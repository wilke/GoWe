package bvbrc

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mintTestToken signs a BV-BRC-style token payload with priv and returns
// the full wire-format token (payload + "|sig=" + hex signature).
func mintTestToken(t *testing.T, priv *rsa.PrivateKey, fields map[string]string, order []string) string {
	t.Helper()
	var parts []string
	for _, k := range order {
		parts = append(parts, k+"="+fields[k])
	}
	payload := strings.Join(parts, "|")
	digest := sha1.Sum([]byte(payload))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA1, digest[:])
	if err != nil {
		t.Fatalf("sign test token: %v", err)
	}
	return payload + "|sig=" + hex.EncodeToString(sig)
}

// standardFields returns a typical set of BV-BRC token fields, ready for
// mintTestToken, targeting signingSubject and expiring at expiry.
func standardFields(username, signingSubject string, expiry time.Time) (map[string]string, []string) {
	fields := map[string]string{
		"un":             username,
		"tokenid":        "11111111-1111-1111-1111-111111111111",
		"expiry":         strconv.FormatInt(expiry.Unix(), 10),
		"client_id":      "test-client",
		"token_type":     "Bearer",
		"realm":          "patricbrc.org",
		"SigningSubject": signingSubject,
	}
	order := []string{"un", "tokenid", "expiry", "client_id", "token_type", "realm", "SigningSubject"}
	return fields, order
}

func genKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	return key
}

func pemPKIX(t *testing.T, pub *rsa.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	block := &pem.Block{Type: "PUBLIC KEY", Bytes: der}
	return string(pem.EncodeToMemory(block))
}

// keyServer variants: how the key endpoint serves its response.
type keyServerMode int

const (
	modeJSON keyServerMode = iota
	modeRawPEM
	modeJSONLiteralNewline
)

// newKeyServer serves pub's PEM in the requested mode and counts requests.
func newKeyServer(t *testing.T, pub *rsa.PublicKey, mode keyServerMode) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		pemText := pemPKIX(t, pub)
		switch mode {
		case modeRawPEM:
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, pemText)
		case modeJSONLiteralNewline:
			literal := strings.ReplaceAll(pemText, "\n", `\n`)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"pubkey": literal})
		default: // modeJSON
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"pubkey": pemText})
		}
	}))
	return srv, &hits
}

func TestVerifier_ValidToken(t *testing.T) {
	for _, mode := range []keyServerMode{modeJSON, modeRawPEM, modeJSONLiteralNewline} {
		mode := mode
		t.Run(fmt.Sprintf("mode=%d", mode), func(t *testing.T) {
			priv := genKey(t)
			srv, _ := newKeyServer(t, &priv.PublicKey, mode)
			defer srv.Close()

			expiry := time.Now().Add(time.Hour)
			fields, order := standardFields("alice@patricbrc.org", srv.URL, expiry)
			token := mintTestToken(t, priv, fields, order)

			v := NewVerifier(WithAllowlist(srv.URL))
			got, err := v.Verify(context.Background(), token)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if got.Username != "alice@patricbrc.org" {
				t.Errorf("Username = %q", got.Username)
			}
			if got.TokenID != fields["tokenid"] {
				t.Errorf("TokenID = %q", got.TokenID)
			}
			if got.Expiry.Unix() != expiry.Unix() {
				t.Errorf("Expiry = %v, want %v", got.Expiry, expiry)
			}
		})
	}
}

func TestVerifier_TamperedUsername(t *testing.T) {
	priv := genKey(t)
	srv, _ := newKeyServer(t, &priv.PublicKey, modeJSON)
	defer srv.Close()

	fields, order := standardFields("alice@patricbrc.org", srv.URL, time.Now().Add(time.Hour))
	token := mintTestToken(t, priv, fields, order)

	// Flip un= after signing without re-signing.
	tampered := strings.Replace(token, "un=alice@patricbrc.org", "un=mallory@patricbrc.org", 1)

	v := NewVerifier(WithAllowlist(srv.URL))
	_, err := v.Verify(context.Background(), tampered)
	if !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

func TestVerifier_AppendedFieldAfterSig(t *testing.T) {
	priv := genKey(t)
	srv, _ := newKeyServer(t, &priv.PublicKey, modeJSON)
	defer srv.Close()

	fields, order := standardFields("alice@patricbrc.org", srv.URL, time.Now().Add(time.Hour))
	token := mintTestToken(t, priv, fields, order)
	withAppended := token + "|un=eve"

	v := NewVerifier(WithAllowlist(srv.URL))
	got, err := v.Verify(context.Background(), withAppended)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Username != "alice@patricbrc.org" {
		t.Errorf("Username = %q, want original signed user (post-sig field must be discarded)", got.Username)
	}
}

func TestVerifier_DuplicateFieldFirstWins(t *testing.T) {
	priv := genKey(t)
	srv, _ := newKeyServer(t, &priv.PublicKey, modeJSON)
	defer srv.Close()

	// Signed payload itself contains a duplicated un= field.
	payload := "un=alice|un=bob|tokenid=22222222-2222-2222-2222-222222222222|expiry=" +
		strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + "|SigningSubject=" + srv.URL
	digest := sha1.Sum([]byte(payload))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA1, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	token := payload + "|sig=" + hex.EncodeToString(sig)

	v := NewVerifier(WithAllowlist(srv.URL))
	got, err := v.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.Username != "alice" {
		t.Errorf("Username = %q, want %q (first occurrence wins)", got.Username, "alice")
	}
}

func TestVerifier_UnpinnedSigningSubject_NoNetworkCall(t *testing.T) {
	priv := genKey(t)
	srv, hits := newKeyServer(t, &priv.PublicKey, modeJSON)
	defer srv.Close()

	// SigningSubject points at a URL NOT in the allowlist.
	fields, order := standardFields("alice@patricbrc.org", srv.URL+"/not-allowlisted", time.Now().Add(time.Hour))
	token := mintTestToken(t, priv, fields, order)

	v := NewVerifier(WithAllowlist(srv.URL)) // allowlist has srv.URL, not srv.URL+"/not-allowlisted"
	_, err := v.Verify(context.Background(), token)
	if !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("key server hits = %d, want 0 (must not fetch key for unpinned issuer)", got)
	}
}

func TestVerifier_ExpiryVariants(t *testing.T) {
	priv := genKey(t)
	srv, _ := newKeyServer(t, &priv.PublicKey, modeJSON)
	defer srv.Close()
	v := NewVerifier(WithAllowlist(srv.URL))

	t.Run("expired", func(t *testing.T) {
		fields, order := standardFields("alice", srv.URL, time.Now().Add(-time.Hour))
		token := mintTestToken(t, priv, fields, order)
		_, err := v.Verify(context.Background(), token)
		if !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("err = %v, want ErrTokenInvalid", err)
		}
	})

	t.Run("missing", func(t *testing.T) {
		payload := "un=alice|tokenid=x|SigningSubject=" + srv.URL
		digest := sha1.Sum([]byte(payload))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA1, digest[:])
		token := payload + "|sig=" + hex.EncodeToString(sig)
		_, err := v.Verify(context.Background(), token)
		if !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("err = %v, want ErrTokenInvalid", err)
		}
	})

	t.Run("non-numeric", func(t *testing.T) {
		payload := "un=alice|tokenid=x|expiry=not-a-number|SigningSubject=" + srv.URL
		digest := sha1.Sum([]byte(payload))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA1, digest[:])
		token := payload + "|sig=" + hex.EncodeToString(sig)
		_, err := v.Verify(context.Background(), token)
		if !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("err = %v, want ErrTokenInvalid", err)
		}
	})
}

func TestVerifier_MalformedSignature(t *testing.T) {
	priv := genKey(t)
	srv, _ := newKeyServer(t, &priv.PublicKey, modeJSON)
	defer srv.Close()
	v := NewVerifier(WithAllowlist(srv.URL))

	fields, order := standardFields("alice", srv.URL, time.Now().Add(time.Hour))
	base := strings.Join(func() []string {
		var parts []string
		for _, k := range order {
			parts = append(parts, k+"="+fields[k])
		}
		return parts
	}(), "|")

	t.Run("non-hex sig", func(t *testing.T) {
		token := base + "|sig=not-hex-zzz"
		_, err := v.Verify(context.Background(), token)
		if !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("err = %v, want ErrTokenInvalid", err)
		}
	})

	t.Run("empty sig", func(t *testing.T) {
		token := base + "|sig="
		_, err := v.Verify(context.Background(), token)
		if !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("err = %v, want ErrTokenInvalid", err)
		}
	})

	t.Run("missing sig separator entirely", func(t *testing.T) {
		_, err := v.Verify(context.Background(), base)
		if !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("err = %v, want ErrTokenInvalid", err)
		}
	})
}

func TestVerifier_KeyRotation(t *testing.T) {
	keyA := genKey(t)
	keyB := genKey(t)

	currentPub := &keyA.PublicKey
	var hits atomic.Int64
	mu := &httpServeMux{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		pemText := pemPKIX(t, mu.get(currentPub))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"pubkey": pemText})
	}))
	defer srv.Close()

	t.Run("rotation succeeds via refetch", func(t *testing.T) {
		hits.Store(0)
		mu.set(&keyA.PublicKey)
		// MinRefetch=0 so the rotation refetch is never rate-limited by the
		// cold-start fetch that just happened.
		v := NewVerifier(WithAllowlist(srv.URL), WithMinRefetch(0))

		// Token signed with key B, while server currently serves key A.
		fields, order := standardFields("alice", srv.URL, time.Now().Add(time.Hour))
		token := mintTestToken(t, keyB, fields, order)

		// First verify: server serves key A, signature is under key B -> fails
		// initial check, triggers a rate-limited refetch (server still A) ->
		// still fails -> ErrTokenInvalid.
		_, err := v.Verify(context.Background(), token)
		if !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("err = %v, want ErrTokenInvalid before rotation", err)
		}

		// Now the issuer rotates to key B.
		mu.set(&keyB.PublicKey)

		got, err := v.Verify(context.Background(), token)
		if err != nil {
			t.Fatalf("Verify after rotation: %v", err)
		}
		if got.Username != "alice" {
			t.Errorf("Username = %q", got.Username)
		}
	})

	t.Run("refetch is rate-limited", func(t *testing.T) {
		hits.Store(0)
		mu.set(&keyA.PublicKey)
		v := NewVerifier(WithAllowlist(srv.URL), WithMinRefetch(time.Hour))

		fields, order := standardFields("alice", srv.URL, time.Now().Add(time.Hour))
		badToken := mintTestToken(t, keyB, fields, order) // never verifies against key A

		_, err := v.Verify(context.Background(), badToken)
		if !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("err = %v, want ErrTokenInvalid", err)
		}
		afterFirst := hits.Load()
		if afterFirst != 1 {
			t.Fatalf("hits after first verify = %d, want 1 (cold-start fetch only)", afterFirst)
		}

		// Repeated garbage signatures must not trigger additional fetches
		// within MinRefetch.
		_, err = v.Verify(context.Background(), badToken)
		if !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("err = %v, want ErrTokenInvalid", err)
		}
		if got := hits.Load(); got != afterFirst {
			t.Errorf("hits after second verify = %d, want unchanged %d (rate-limited)", got, afterFirst)
		}
	})
}

// httpServeMux is a tiny mutex-guarded box so the key-rotation test server
// can swap which key it serves mid-test.
type httpServeMux struct {
	pub *rsa.PublicKey
}

func (m *httpServeMux) set(pub *rsa.PublicKey) { m.pub = pub }
func (m *httpServeMux) get(fallback *rsa.PublicKey) *rsa.PublicKey {
	if m.pub != nil {
		return m.pub
	}
	return fallback
}

func TestVerifier_KeyServerDown(t *testing.T) {
	priv := genKey(t)

	t.Run("no cache -> ErrKeyUnavailable", func(t *testing.T) {
		srv, _ := newKeyServer(t, &priv.PublicKey, modeJSON)
		url := srv.URL
		srv.Close() // down before any fetch happens

		fields, order := standardFields("alice", url, time.Now().Add(time.Hour))
		token := mintTestToken(t, priv, fields, order)

		v := NewVerifier(WithAllowlist(url))
		_, err := v.Verify(context.Background(), token)
		if !errors.Is(err, ErrKeyUnavailable) {
			t.Fatalf("err = %v, want ErrKeyUnavailable", err)
		}
	})

	t.Run("warm cache past TTL -> stale key used", func(t *testing.T) {
		srv, _ := newKeyServer(t, &priv.PublicKey, modeJSON)
		defer func() {
			if srv != nil {
				srv.Close()
			}
		}()

		v := NewVerifier(WithAllowlist(srv.URL), WithKeyTTL(10*time.Millisecond))

		fields, order := standardFields("alice", srv.URL, time.Now().Add(time.Hour))
		token := mintTestToken(t, priv, fields, order)

		// Warm the cache.
		if _, err := v.Verify(context.Background(), token); err != nil {
			t.Fatalf("warm-up Verify: %v", err)
		}

		// Let the cached key go stale, then take the server down.
		time.Sleep(20 * time.Millisecond)
		srv.Close()
		srv = nil

		// New token so it re-validates the signature against the stale key.
		fields2, order2 := standardFields("alice", "", time.Now().Add(time.Hour))
		fields2["SigningSubject"] = fields["SigningSubject"]
		token2 := mintTestToken(t, priv, fields2, order2)

		got, err := v.Verify(context.Background(), token2)
		if err != nil {
			t.Fatalf("Verify with stale key: %v", err)
		}
		if got.Username != "alice" {
			t.Errorf("Username = %q", got.Username)
		}
	})
}
