package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/me/gowe/internal/tokencrypt"
	"github.com/me/gowe/pkg/model"
)

// ConfigureTokenEncryption sets the at-rest encryption policy for persisted
// provider tokens: the submitter's BV-BRC/MG-RAST token in
// submissions.user_token and any HTTP bearer credential embedded in a task's
// runtime hints (tasks.runtime_hints).
//
//   - cipher != nil:  tokens are encrypted before persistence and decrypted on
//     read. Rows written in plaintext before encryption was enabled are read
//     transparently and can be upgraded with ReencryptPlaintextTokens.
//   - cipher == nil && refusePlaintext:  the store fails closed — persisting a
//     non-empty token returns an error instead of writing it in the clear.
//   - cipher == nil && !refusePlaintext: legacy behavior — tokens are stored in
//     plaintext (a single warning is logged on first write).
//
// In-memory Submission/Task values always carry the plaintext token; encryption
// is confined to the database boundary so the delegated-execution path (local
// executor and the worker API) is unchanged.
func (s *SQLiteStore) ConfigureTokenEncryption(cipher *tokencrypt.Cipher, refusePlaintext bool) {
	s.cipher = cipher
	s.refusePlaintextTokens = refusePlaintext
}

// submissionTokenAAD and taskHintsAAD build the context strings that bind a
// ciphertext to the row/column it is stored in (AES-GCM AAD, see tokencrypt).
// They MUST be stable for a given row across encrypt and decrypt.
func submissionTokenAAD(id string) string { return "submission.user_token:" + id }
func taskHintsAAD(id string) string       { return "task.runtime_hints.http_credential:" + id }

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

// marshalRuntimeHints marshals task runtime hints for persistence, encrypting an
// embedded HTTP bearer token when required by policy.
func (s *SQLiteStore) marshalRuntimeHints(h *model.RuntimeHints, aad string) (string, error) {
	stored, err := s.runtimeHintsForStorage(h, aad)
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
// HTTP bearer token encrypted for storage. The input is never mutated, so
// callers keep operating on the live, plaintext token in memory. Returns h
// unchanged when there is no token to protect.
func (s *SQLiteStore) runtimeHintsForStorage(h *model.RuntimeHints, aad string) (*model.RuntimeHints, error) {
	if h == nil || h.StagerOverrides == nil || h.StagerOverrides.HTTPCredential == nil {
		return h, nil
	}
	if h.StagerOverrides.HTTPCredential.Token == "" {
		return h, nil
	}
	enc, err := s.encryptToken(h.StagerOverrides.HTTPCredential.Token, aad)
	if err != nil {
		return nil, err
	}
	// Copy only along the path we mutate; everything else is shared.
	hc := *h
	so := *h.StagerOverrides
	cred := *h.StagerOverrides.HTTPCredential
	cred.Token = enc
	so.HTTPCredential = &cred
	hc.StagerOverrides = &so
	return &hc, nil
}

// revealRuntimeHints decrypts an embedded HTTP bearer token in freshly-scanned
// task runtime hints, in place. Safe because each read produces a fresh object.
func (s *SQLiteStore) revealRuntimeHints(h *model.RuntimeHints, aad string) error {
	if h == nil || h.StagerOverrides == nil || h.StagerOverrides.HTTPCredential == nil {
		return nil
	}
	cred := h.StagerOverrides.HTTPCredential
	if cred.Token == "" {
		return nil
	}
	pt, err := s.decryptToken(cred.Token, aad)
	if err != nil {
		return err
	}
	cred.Token = pt
	return nil
}

// ReencryptPlaintextTokens upgrades provider tokens that were written in
// plaintext (before encryption was enabled) to ciphertext, in both
// submissions.user_token and tasks.runtime_hints. It is a no-op when no cipher
// is configured. Per-row failures are logged (never the token value) and
// skipped so one bad row does not abort startup. Returns the number of
// submission and task rows rewritten.
func (s *SQLiteStore) ReencryptPlaintextTokens(ctx context.Context) (subs int, tasks int, err error) {
	if s.cipher == nil {
		return 0, 0, nil
	}

	type update struct{ id, val string }

	// --- submissions.user_token ---
	// Collect first, then write: with a single writer connection we must finish
	// iterating before issuing UPDATEs.
	var subUpdates []update
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
		subUpdates = append(subUpdates, update{id, enc})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return subs, tasks, err
	}
	for _, u := range subUpdates {
		if _, err := s.db.ExecContext(ctx, `UPDATE submissions SET user_token=? WHERE id=?`, u.val, u.id); err != nil {
			s.logger.Error("update submission token", "id", u.id, "error", err)
			continue
		}
		subs++
	}

	// --- tasks.runtime_hints (embedded bearer token) ---
	var taskUpdates []update
	trows, err := s.db.QueryContext(ctx, `SELECT id, runtime_hints FROM tasks WHERE runtime_hints LIKE '%http_credential%'`)
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
		if h.StagerOverrides == nil || h.StagerOverrides.HTTPCredential == nil {
			continue
		}
		tok := h.StagerOverrides.HTTPCredential.Token
		if tok == "" || (tokencrypt.IsEncrypted(tok) && !tokencrypt.NeedsAADUpgrade(tok)) {
			continue
		}
		aad := taskHintsAAD(id)
		pt, decErr := s.cipher.Decrypt(tok, aad)
		if decErr != nil {
			s.logger.Error("re-encrypt task token", "id", id, "error", decErr)
			continue
		}
		enc, encErr := s.cipher.Encrypt(pt, aad)
		if encErr != nil {
			s.logger.Error("re-encrypt task token", "id", id, "error", encErr)
			continue
		}
		h.StagerOverrides.HTTPCredential.Token = enc
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
