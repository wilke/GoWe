package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/me/gowe/internal/tokencrypt"
	"github.com/me/gowe/pkg/model"
)

// ConfigureTokenEncryption sets the at-rest encryption policy for persisted
// provider tokens and submission-time secrets: the submitter's BV-BRC/MG-RAST
// token in submissions.user_token, any HTTP bearer credential embedded in a
// task's runtime hints (tasks.runtime_hints), the submission's secrets map
// (submissions.secrets), and the per-task subset of secrets embedded in a
// task's runtime hints (tasks.runtime_hints).
//
//   - cipher != nil:  values are encrypted before persistence and decrypted on
//     read. Rows written in plaintext before encryption was enabled are read
//     transparently and can be upgraded with ReencryptPlaintextTokens.
//   - cipher == nil && refusePlaintext:  the store fails closed — persisting a
//     non-empty token/secret returns an error instead of writing it in the clear.
//   - cipher == nil && !refusePlaintext: legacy behavior — values are stored in
//     plaintext (a single warning is logged on first write).
//
// In-memory Submission/Task values always carry plaintext; encryption is
// confined to the database boundary so the delegated-execution path (local
// executor and the worker API) is unchanged.
func (s *SQLiteStore) ConfigureTokenEncryption(cipher *tokencrypt.Cipher, refusePlaintext bool) {
	s.cipher = cipher
	s.refusePlaintextTokens = refusePlaintext
}

// submissionTokenAAD, taskHintsAAD, submissionSecretsAAD, and taskSecretsAAD
// build the context strings that bind a ciphertext to the row/column it is
// stored in (AES-GCM AAD, see tokencrypt). They MUST be stable for a given
// row across encrypt and decrypt.
func submissionTokenAAD(id string) string   { return "submission.user_token:" + id }
func taskHintsAAD(id string) string         { return "task.runtime_hints.http_credential:" + id }
func submissionSecretsAAD(id string) string { return "submission.secrets:" + id }
func taskSecretsAAD(id string) string       { return "task.runtime_hints.secrets:" + id }

// encryptToken prepares a token for persistence per the configured policy,
// binding aad as the storage context so the ciphertext cannot be relocated to
// another row.
func (s *SQLiteStore) encryptToken(token, aad string) (string, error) {
	if token == "" {
		return "", nil
	}
	if s.cipher != nil {
		return s.cipher.Encrypt(token, aad)
	}
	if s.refusePlaintextTokens {
		return "", fmt.Errorf("refusing to persist provider token in plaintext: no encryption key configured (set %s, or start the server with --allow-plaintext-tokens to override)", tokencrypt.EnvKeyVar)
	}
	s.plaintextWarnOnce.Do(func() {
		s.logger.Warn("persisting provider tokens in plaintext at rest; set " + tokencrypt.EnvKeyVar + " to encrypt them")
	})
	return token, nil
}

// decryptToken reverses encryptToken for a value read from the database.
// Without a cipher, a plaintext value passes through, but a value that is
// marked as encrypted is an error (the key is missing/misconfigured) rather
// than something to hand downstream as if it were a real token.
func (s *SQLiteStore) decryptToken(stored, aad string) (string, error) {
	if stored == "" {
		return "", nil
	}
	if s.cipher != nil {
		return s.cipher.Decrypt(stored, aad)
	}
	if tokencrypt.IsEncrypted(stored) {
		return "", fmt.Errorf("stored token is encrypted but no key is configured (set %s)", tokencrypt.EnvKeyVar)
	}
	return stored, nil
}

// encryptSecretsMap marshals a secrets map (name -> value) to JSON and
// encrypts it under aad per the configured token policy, following the same
// fail-closed/plaintext-warning rules as encryptToken. A nil/empty map
// encrypts to "" (the caller stores that as SQL NULL).
func (s *SQLiteStore) encryptSecretsMap(m map[string]string, aad string) (string, error) {
	if len(m) == 0 {
		return "", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal secrets: %w", err)
	}
	return s.encryptToken(string(b), aad)
}

// decryptSecretsMap reverses encryptSecretsMap. An empty stored value decodes
// to a nil map, no error.
func (s *SQLiteStore) decryptSecretsMap(stored, aad string) (map[string]string, error) {
	if stored == "" {
		return nil, nil
	}
	pt, err := s.decryptToken(stored, aad)
	if err != nil {
		return nil, err
	}
	if pt == "" {
		return nil, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(pt), &m); err != nil {
		return nil, fmt.Errorf("corrupt secrets JSON: %w", err)
	}
	return m, nil
}

// marshalRuntimeHints marshals task runtime hints for persistence, encrypting
// an embedded HTTP bearer token and/or the task's delivered secrets subset
// when required by policy. id is the task ID, used to derive both AADs.
func (s *SQLiteStore) marshalRuntimeHints(h *model.RuntimeHints, id string) (string, error) {
	stored, err := s.runtimeHintsForStorage(h, id)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(stored)
	if err != nil {
		return "", fmt.Errorf("marshal runtime_hints: %w", err)
	}
	return string(b), nil
}

// runtimeHintsForStorage returns a value equivalent to h but with any embedded
// HTTP bearer token encrypted, and h.Secrets (if non-empty) replaced by the
// single-entry map {"__enc__": "<encrypted-or-passthrough JSON blob>"} — see
// revealRuntimeHints for the read-side counterpart. The input is never
// mutated, so callers keep operating on the live, plaintext values in memory.
// Returns h unchanged when there is nothing to protect.
func (s *SQLiteStore) runtimeHintsForStorage(h *model.RuntimeHints, id string) (*model.RuntimeHints, error) {
	if h == nil {
		return h, nil
	}
	hasCred := h.StagerOverrides != nil && h.StagerOverrides.HTTPCredential != nil && h.StagerOverrides.HTTPCredential.Token != ""
	hasSecrets := len(h.Secrets) > 0
	if !hasCred && !hasSecrets {
		return h, nil
	}

	// Copy only along the paths we mutate; everything else is shared.
	hc := *h

	if hasCred {
		enc, err := s.encryptToken(h.StagerOverrides.HTTPCredential.Token, taskHintsAAD(id))
		if err != nil {
			return nil, err
		}
		so := *h.StagerOverrides
		cred := *h.StagerOverrides.HTTPCredential
		cred.Token = enc
		so.HTTPCredential = &cred
		hc.StagerOverrides = &so
	}

	if hasSecrets {
		enc, err := s.encryptSecretsMap(h.Secrets, taskSecretsAAD(id))
		if err != nil {
			return nil, err
		}
		hc.Secrets = map[string]string{"__enc__": enc}
	}

	return &hc, nil
}

// revealRuntimeHints decrypts an embedded HTTP bearer token and/or the
// task's secrets subset in freshly-scanned task runtime hints, in place.
// Safe because each read produces a fresh object. id is the task ID, used to
// derive both AADs.
func (s *SQLiteStore) revealRuntimeHints(h *model.RuntimeHints, id string) error {
	if h == nil {
		return nil
	}
	if h.StagerOverrides != nil && h.StagerOverrides.HTTPCredential != nil && h.StagerOverrides.HTTPCredential.Token != "" {
		pt, err := s.decryptToken(h.StagerOverrides.HTTPCredential.Token, taskHintsAAD(id))
		if err != nil {
			return err
		}
		h.StagerOverrides.HTTPCredential.Token = pt
	}
	if enc, ok := h.Secrets["__enc__"]; ok {
		m, err := s.decryptSecretsMap(enc, taskSecretsAAD(id))
		if err != nil {
			return err
		}
		h.Secrets = m
	}
	return nil
}

// ReencryptPlaintextTokens upgrades provider tokens and submission-time
// secrets that were written in plaintext (before encryption was enabled) to
// ciphertext, across submissions.user_token, submissions.secrets, and
// tasks.runtime_hints (both the embedded HTTP bearer token and the embedded
// secrets subset). It is a no-op when no cipher is configured. Per-row
// failures are logged (never the secret value) and skipped so one bad row
// does not abort startup. Returns the number of submission and task rows
// rewritten (a row counts once even if it needed more than one field
// upgraded).
func (s *SQLiteStore) ReencryptPlaintextTokens(ctx context.Context) (subs int, tasks int, err error) {
	if s.cipher == nil {
		return 0, 0, nil
	}

	type update struct{ id, val string }

	// --- submissions.user_token ---
	// Collect first, then write: with a single writer connection we must finish
	// iterating before issuing UPDATEs.
	var subTokenUpdates []update
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_token FROM submissions WHERE user_token != ''`)
	if err != nil {
		return 0, 0, fmt.Errorf("scan submissions: %w", err)
	}
	for rows.Next() {
		var id, tok string
		if err := rows.Scan(&id, &tok); err != nil {
			rows.Close()
			return subs, tasks, fmt.Errorf("scan submission token: %w", err)
		}
		// Already in the current AAD-bound format: nothing to do.
		if tokencrypt.IsEncrypted(tok) && !tokencrypt.NeedsAADUpgrade(tok) {
			continue
		}
		aad := submissionTokenAAD(id)
		// Normalize plaintext or legacy v1 to v2. Decrypt passes plaintext
		// through and decrypts v1 with no AAD; Encrypt re-binds it to this row.
		pt, decErr := s.cipher.Decrypt(tok, aad)
		if decErr != nil {
			s.logger.Error("re-encrypt submission token", "id", id, "error", decErr)
			continue
		}
		enc, encErr := s.cipher.Encrypt(pt, aad)
		if encErr != nil {
			s.logger.Error("re-encrypt submission token", "id", id, "error", encErr)
			continue
		}
		subTokenUpdates = append(subTokenUpdates, update{id, enc})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return subs, tasks, err
	}
	subRewritten := make(map[string]bool, len(subTokenUpdates))
	for _, u := range subTokenUpdates {
		if _, err := s.db.ExecContext(ctx, `UPDATE submissions SET user_token=? WHERE id=?`, u.val, u.id); err != nil {
			s.logger.Error("update submission token", "id", u.id, "error", err)
			continue
		}
		subRewritten[u.id] = true
	}

	// --- submissions.secrets ---
	var subSecretsUpdates []update
	srows, err := s.db.QueryContext(ctx, `SELECT id, secrets FROM submissions WHERE secrets IS NOT NULL AND secrets != ''`)
	if err != nil {
		return subs, tasks, fmt.Errorf("scan submission secrets: %w", err)
	}
	for srows.Next() {
		var id, val string
		if err := srows.Scan(&id, &val); err != nil {
			srows.Close()
			return subs, tasks, fmt.Errorf("scan submission secrets: %w", err)
		}
		if tokencrypt.IsEncrypted(val) && !tokencrypt.NeedsAADUpgrade(val) {
			continue
		}
		aad := submissionSecretsAAD(id)
		pt, decErr := s.cipher.Decrypt(val, aad)
		if decErr != nil {
			s.logger.Error("re-encrypt submission secrets", "id", id, "error", decErr)
			continue
		}
		enc, encErr := s.cipher.Encrypt(pt, aad)
		if encErr != nil {
			s.logger.Error("re-encrypt submission secrets", "id", id, "error", encErr)
			continue
		}
		subSecretsUpdates = append(subSecretsUpdates, update{id, enc})
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return subs, tasks, err
	}
	for _, u := range subSecretsUpdates {
		if _, err := s.db.ExecContext(ctx, `UPDATE submissions SET secrets=? WHERE id=?`, u.val, u.id); err != nil {
			s.logger.Error("update submission secrets", "id", u.id, "error", err)
			continue
		}
		subRewritten[u.id] = true
	}
	subs = len(subRewritten)

	// --- tasks.runtime_hints (embedded bearer token and/or secrets) ---
	var taskUpdates []update
	trows, err := s.db.QueryContext(ctx, `SELECT id, runtime_hints FROM tasks WHERE runtime_hints LIKE '%http_credential%' OR runtime_hints LIKE '%__enc__%'`)
	if err != nil {
		return subs, tasks, fmt.Errorf("scan tasks: %w", err)
	}
	for trows.Next() {
		var id, hintsJSON string
		if err := trows.Scan(&id, &hintsJSON); err != nil {
			trows.Close()
			return subs, tasks, fmt.Errorf("scan task hints: %w", err)
		}
		var h model.RuntimeHints
		if err := json.Unmarshal([]byte(hintsJSON), &h); err != nil {
			continue
		}
		changed := false

		if h.StagerOverrides != nil && h.StagerOverrides.HTTPCredential != nil {
			tok := h.StagerOverrides.HTTPCredential.Token
			if tok != "" && !(tokencrypt.IsEncrypted(tok) && !tokencrypt.NeedsAADUpgrade(tok)) {
				aad := taskHintsAAD(id)
				pt, decErr := s.cipher.Decrypt(tok, aad)
				if decErr != nil {
					s.logger.Error("re-encrypt task token", "id", id, "error", decErr)
				} else {
					enc, encErr := s.cipher.Encrypt(pt, aad)
					if encErr != nil {
						s.logger.Error("re-encrypt task token", "id", id, "error", encErr)
					} else {
						h.StagerOverrides.HTTPCredential.Token = enc
						changed = true
					}
				}
			}
		}

		if enc, ok := h.Secrets["__enc__"]; ok && enc != "" && !(tokencrypt.IsEncrypted(enc) && !tokencrypt.NeedsAADUpgrade(enc)) {
			aad := taskSecretsAAD(id)
			pt, decErr := s.cipher.Decrypt(enc, aad)
			if decErr != nil {
				s.logger.Error("re-encrypt task secrets", "id", id, "error", decErr)
			} else {
				reenc, encErr := s.cipher.Encrypt(pt, aad)
				if encErr != nil {
					s.logger.Error("re-encrypt task secrets", "id", id, "error", encErr)
				} else {
					h.Secrets["__enc__"] = reenc
					changed = true
				}
			}
		}

		if !changed {
			continue
		}
		nb, mErr := json.Marshal(&h)
		if mErr != nil {
			s.logger.Error("marshal re-encrypted task hints", "id", id, "error", mErr)
			continue
		}
		taskUpdates = append(taskUpdates, update{id, string(nb)})
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return subs, tasks, err
	}
	for _, u := range taskUpdates {
		if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET runtime_hints=? WHERE id=?`, u.val, u.id); err != nil {
			s.logger.Error("update task runtime_hints", "id", u.id, "error", err)
			continue
		}
		tasks++
	}

	return subs, tasks, nil
}
