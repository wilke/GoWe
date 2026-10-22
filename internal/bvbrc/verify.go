package bvbrc

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultSigningSubjects is the hard-pinned set of canonical BV-BRC key-server
// URLs, taken from BV-BRC's P3AuthConstants.pm. A token's SigningSubject field
// must match one of these before any network call is made to fetch a key —
// the field itself is client-supplied and must never be trusted to name its
// own verifying key.
var DefaultSigningSubjects = []string{
	"https://user.patricbrc.org/public_key",
	"https://user.bv-brc.org/public_key",
	"https://user.alpha.patricbrc.org/public_key",
	"https://user.beta.patricbrc.org/public_key",
}

const sigSeparator = "|sig="

// DefaultKeyTTL is how long a fetched issuer public key is cached before a
// normal (non-rotation) refetch is due.
const DefaultKeyTTL = 24 * time.Hour

// DefaultMinRefetch is the minimum interval between key refetches triggered
// by a failed signature verification (key-rotation path). It rate-limits
// refetching so a stream of bad signatures cannot turn into repeated
// requests against the key server.
const DefaultMinRefetch = 60 * time.Second

// ErrTokenInvalid classifies a token that was evaluated and rejected:
// malformed, an unpinned SigningSubject, expired, missing/non-numeric
// expiry, a bad or missing signature, or missing required fields. Callers
// should map this to HTTP 401.
var ErrTokenInvalid = errors.New("bvbrc: token invalid")

// ErrKeyUnavailable classifies a failure to obtain a verifying key: the
// pinned key server is unreachable, or its response is not a usable RSA
// public key. Callers should map this to HTTP 503 — this is a dependency
// outage, not a rejected credential.
var ErrKeyUnavailable = errors.New("bvbrc: verifying key unavailable")

// VerifiedToken carries the fields of a BV-BRC token whose signature has
// been verified against a pinned issuer key. Only fields read after
// signature verification are exposed here.
type VerifiedToken struct {
	Username string
	TokenID  string
	Expiry   time.Time
}

// cachedKey holds a fetched issuer public key plus bookkeeping for TTL and
// rotation-refetch rate limiting.
type cachedKey struct {
	key       *rsa.PublicKey
	fetchedAt time.Time
}

// Verifier verifies BV-BRC provider tokens offline (aside from fetching and
// caching the issuer's public key) against a hard-pinned allowlist of
// SigningSubject URLs.
//
// Order matters and is part of the contract: parse -> check SigningSubject
// against the allowlist (before any network I/O) -> check expiry -> verify
// signature -> only then read un=/tokenid=.
type Verifier struct {
	allowlist  map[string]struct{}
	httpClient *http.Client
	keyTTL     time.Duration
	minRefetch time.Duration
	logger     *slog.Logger

	mu   sync.Mutex
	keys map[string]cachedKey
}

// VerifierOption configures a Verifier.
type VerifierOption func(*Verifier)

// WithAllowlist overrides the default hard-pinned SigningSubject allowlist.
//
// FOR TESTS ONLY: production callers must rely on the default, hard-pinned
// set of canonical BV-BRC issuer URLs. Narrowing or replacing the allowlist
// changes which issuer keys are trusted, so this option exists so tests can
// point at an httptest key server.
func WithAllowlist(urls ...string) VerifierOption {
	return func(v *Verifier) {
		v.allowlist = make(map[string]struct{}, len(urls))
		for _, u := range urls {
			v.allowlist[u] = struct{}{}
		}
	}
}

// WithHTTPClient sets the HTTP client used to fetch issuer public keys.
func WithHTTPClient(c *http.Client) VerifierOption {
	return func(v *Verifier) {
		v.httpClient = c
	}
}

// WithKeyTTL sets how long a fetched key is cached before a normal refetch
// is due.
func WithKeyTTL(d time.Duration) VerifierOption {
	return func(v *Verifier) {
		v.keyTTL = d
	}
}

// WithMinRefetch sets the minimum interval between rotation-path refetches
// (triggered by a failed signature verification) for a given issuer URL.
func WithMinRefetch(d time.Duration) VerifierOption {
	return func(v *Verifier) {
		v.minRefetch = d
	}
}

// WithLogger sets the logger used for warnings (e.g. serving a stale key
// past its TTL because refetch failed). Defaults to slog.Default().
func WithLogger(logger *slog.Logger) VerifierOption {
	return func(v *Verifier) {
		v.logger = logger
	}
}

// NewVerifier creates a Verifier. With no options, it trusts only the
// hard-pinned default SigningSubject allowlist.
func NewVerifier(opts ...VerifierOption) *Verifier {
	v := &Verifier{
		httpClient: &http.Client{Timeout: 5 * time.Second},
		keyTTL:     DefaultKeyTTL,
		minRefetch: DefaultMinRefetch,
		logger:     slog.Default(),
		keys:       make(map[string]cachedKey),
	}
	for _, opt := range opts {
		opt(v)
	}
	if v.allowlist == nil {
		v.allowlist = make(map[string]struct{}, len(DefaultSigningSubjects))
		for _, u := range DefaultSigningSubjects {
			v.allowlist[u] = struct{}{}
		}
	}
	return v
}

// Verify parses and cryptographically verifies raw as a BV-BRC provider
// token. On success it returns the verified username, token ID, and expiry
// — read only after the signature has been checked against a pinned issuer
// key. See the Verifier doc comment for the required check order.
func (v *Verifier) Verify(ctx context.Context, raw string) (*VerifiedToken, error) {
	payload, sigHex, err := splitSignedPayload(raw)
	if err != nil {
		return nil, err
	}
	fields := parseFieldsFirstWins(payload)

	signingSubject := fields["SigningSubject"]
	if _, ok := v.allowlist[signingSubject]; !ok {
		// Before any network call: an unpinned SigningSubject must never
		// cause a key fetch, since the URL is client-supplied.
		return nil, fmt.Errorf("%w: SigningSubject is not an allowed issuer", ErrTokenInvalid)
	}

	expiry, err := parseExpiry(fields)
	if err != nil {
		return nil, err
	}
	if !expiry.After(time.Now()) {
		return nil, fmt.Errorf("%w: token expired", ErrTokenInvalid)
	}

	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) == 0 {
		return nil, fmt.Errorf("%w: signature is not valid hex", ErrTokenInvalid)
	}

	if err := v.verifySignature(ctx, signingSubject, payload, sig); err != nil {
		return nil, err
	}

	// Past this point, and not one line earlier, fields is trustworthy: it
	// was parsed from the exact byte range the signature covers.
	username := fields["un"]
	if username == "" {
		return nil, fmt.Errorf("%w: verified token carries no un= subject", ErrTokenInvalid)
	}
	tokenID := fields["tokenid"]
	if tokenID == "" {
		return nil, fmt.Errorf("%w: verified token carries no tokenid", ErrTokenInvalid)
	}

	return &VerifiedToken{
		Username: username,
		TokenID:  tokenID,
		Expiry:   expiry,
	}, nil
}

// verifySignature verifies sig over payload using the issuer key cached (or
// fetched) for url. On a first failure it performs one rate-limited
// refetch to handle key rotation, then retries once before giving up.
func (v *Verifier) verifySignature(ctx context.Context, url, payload string, sig []byte) error {
	key, err := v.publicKey(ctx, url)
	if err != nil {
		return err
	}
	if signatureOK(key, payload, sig) {
		return nil
	}

	refreshed, err := v.refreshKey(ctx, url)
	if err != nil {
		return err
	}
	if refreshed != nil && signatureOK(refreshed, payload, sig) {
		return nil
	}
	return fmt.Errorf("%w: signature does not verify", ErrTokenInvalid)
}

// publicKey returns the cached key for url if it is within TTL, otherwise
// fetches a fresh one. If a cached key exists but is past TTL and the
// refetch fails, the stale cached key is returned instead of an error.
//
// The mutex is held across the fetch (not just the map access): the scale
// here is small (a handful of pinned issuer URLs) so a plain mutex is
// enough to dedupe concurrent cold-start fetches for the same URL without
// needing a singleflight-style mechanism.
func (v *Verifier) publicKey(ctx context.Context, url string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	cached, ok := v.keys[url]
	if ok && time.Since(cached.fetchedAt) < v.keyTTL {
		return cached.key, nil
	}

	key, err := v.fetchKeyLocked(ctx, url)
	if err != nil {
		if ok {
			v.logger.Warn("bvbrc: using stale cached issuer key after refetch failure",
				"url", url, "cached_age", time.Since(cached.fetchedAt), "error", err)
			return cached.key, nil
		}
		return nil, err
	}
	return key, nil
}

// refreshKey attempts a rate-limited refetch on the key-rotation path
// (triggered by a failed signature). Returns (nil, nil) when the refetch is
// suppressed by the rate limit — that is not an error, it just means no new
// key is available to retry with.
//
// Holding the mutex across the whole check-then-fetch keeps the rate limit
// itself race-free: without it, concurrent callers could all observe "not
// rate-limited yet" and all refetch before any of them records the new
// fetchedAt, defeating the point of the limit.
func (v *Verifier) refreshKey(ctx context.Context, url string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	cached, ok := v.keys[url]
	if ok && time.Since(cached.fetchedAt) < v.minRefetch {
		return nil, nil
	}
	return v.fetchKeyLocked(ctx, url)
}

// fetchKeyLocked fetches and parses the issuer public key from url and
// updates the cache. Callers must hold v.mu. url is always a member of the
// pinned allowlist here, never a value taken from the token.
func (v *Verifier) fetchKeyLocked(ctx context.Context, url string) (*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build key request: %v", ErrKeyUnavailable, err)
	}
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch issuer key from %s: %v", ErrKeyUnavailable, url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: read issuer key response from %s: %v", ErrKeyUnavailable, url, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: issuer key server %s returned HTTP %d", ErrKeyUnavailable, url, resp.StatusCode)
	}

	pemText, err := extractPEM(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKeyUnavailable, err)
	}

	key, err := parseRSAPublicKeyPEM(pemText)
	if err != nil {
		return nil, fmt.Errorf("%w: issuer key at %s: %v", ErrKeyUnavailable, url, err)
	}

	v.keys[url] = cachedKey{key: key, fetchedAt: time.Now()}
	return key, nil
}

// keyResponse matches the JSON shape a key server may respond with.
type keyResponse struct {
	PubKey    string `json:"pubkey"`
	PublicKey string `json:"public_key"`
}

// extractPEM pulls the PEM text out of a key-server response body, which
// may be JSON ({"pubkey": ...} or {"public_key": ...}) or raw PEM text.
// Some deployments serve the PEM with literal backslash-n two-character
// sequences instead of real newlines.
func extractPEM(body []byte) (string, error) {
	var kr keyResponse
	if err := json.Unmarshal(body, &kr); err == nil {
		pem := kr.PubKey
		if pem == "" {
			pem = kr.PublicKey
		}
		if pem == "" {
			return "", errors.New("key server JSON response has no pubkey/public_key field")
		}
		return normalizePEM(pem), nil
	}
	return normalizePEM(string(body)), nil
}

func normalizePEM(s string) string {
	s = strings.ReplaceAll(s, `\n`, "\n")
	return strings.TrimSpace(s)
}

// parseRSAPublicKeyPEM parses PEM text as an RSA public key, trying PKIX
// (SubjectPublicKeyInfo) first and falling back to PKCS#1.
func parseRSAPublicKeyPEM(pemText string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("no PEM block found")
	}

	if pub, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		rsaKey, ok := pub.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("key is not an RSA key")
		}
		return rsaKey, nil
	}

	if rsaKey, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return rsaKey, nil
	}

	return nil, errors.New("unparseable public key")
}

// signatureOK reports whether sig is a valid RSA PKCS#1 v1.5 / SHA-1
// signature over payload under key. SHA-1 is what BV-BRC signs with; this
// is a compatibility constraint of the existing token scheme, not a choice
// made here.
func signatureOK(key *rsa.PublicKey, payload string, sig []byte) bool {
	digest := sha1.Sum([]byte(payload))
	return rsa.VerifyPKCS1v15(key, crypto.SHA1, digest[:], sig) == nil
}

// splitSignedPayload splits raw into the signed payload (everything before
// the first "|sig=") and the signature's hex text (up to the next "|").
// Anything after that is outside the signed region and is discarded rather
// than parsed.
func splitSignedPayload(raw string) (payload, sigHex string, err error) {
	payload, tail, found := strings.Cut(raw, sigSeparator)
	if !found || payload == "" {
		return "", "", fmt.Errorf("%w: malformed token: no signature", ErrTokenInvalid)
	}
	sigHex, _, _ = strings.Cut(tail, "|")
	sigHex = strings.TrimSpace(sigHex)
	if sigHex == "" {
		return "", "", fmt.Errorf("%w: malformed token: empty signature", ErrTokenInvalid)
	}
	return payload, sigHex, nil
}

// parseFieldsFirstWins parses "k=v|k=v" into a map where the first
// occurrence of a key wins, so a duplicated field cannot shadow the one a
// validator reads.
func parseFieldsFirstWins(payload string) map[string]string {
	fields := make(map[string]string)
	for _, part := range strings.Split(payload, "|") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		if _, exists := fields[k]; !exists {
			fields[k] = v
		}
	}
	return fields
}

// parseExpiry reads and validates the expiry field. A token with no expiry
// field is rejected rather than treated as immortal.
func parseExpiry(fields map[string]string) (time.Time, error) {
	raw, ok := fields["expiry"]
	if !ok || raw == "" {
		return time.Time{}, fmt.Errorf("%w: token carries no expiry", ErrTokenInvalid)
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: token expiry is not a number", ErrTokenInvalid)
	}
	return time.Unix(int64(f), 0), nil
}
