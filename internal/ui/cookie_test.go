package ui

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

func TestResolveSecureCookie(t *testing.T) {
	tests := []struct {
		name                string
		tls                 bool
		forwardedProto      string
		forceSecure         bool
		trustForwardedProto bool
		want                bool
	}{
		{name: "plain http, no config", want: false},
		{name: "force secure", forceSecure: true, want: true},
		{name: "native tls", tls: true, want: true},
		{name: "native tls wins even without force", tls: true, forceSecure: false, want: true},
		{name: "forwarded https trusted", forwardedProto: "https", trustForwardedProto: true, want: true},
		{name: "forwarded https case-insensitive", forwardedProto: "HTTPS", trustForwardedProto: true, want: true},
		{name: "forwarded https not trusted", forwardedProto: "https", trustForwardedProto: false, want: false},
		{name: "forwarded http trusted", forwardedProto: "http", trustForwardedProto: true, want: false},
		{name: "spoofed forwarded ignored when untrusted", forwardedProto: "https", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.forwardedProto != "" {
				r.Header.Set("X-Forwarded-Proto", tt.forwardedProto)
			}
			if tt.tls {
				// A non-nil TLS connection state marks the request as HTTPS.
				r.TLS = &tls.ConnectionState{}
			}
			got := resolveSecureCookie(r, tt.forceSecure, tt.trustForwardedProto)
			if got != tt.want {
				t.Fatalf("resolveSecureCookie() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetSessionCookieSecureAttribute(t *testing.T) {
	sess := &model.Session{ID: "abc123", ExpiresAt: time.Now().Add(time.Hour)}

	for _, secure := range []bool{true, false} {
		rec := httptest.NewRecorder()
		SetSessionCookie(rec, sess, secure, "")

		cookies := rec.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("expected 1 cookie, got %d", len(cookies))
		}
		c := cookies[0]
		if c.Name != SessionCookieName {
			t.Fatalf("cookie name = %q, want %q", c.Name, SessionCookieName)
		}
		if c.Secure != secure {
			t.Fatalf("cookie Secure = %v, want %v", c.Secure, secure)
		}
		if !c.HttpOnly {
			t.Fatalf("session cookie must be HttpOnly")
		}
	}
}

// TestSessionCookiePathUnderBasePath is the #250 contract: SetSessionCookie
// and ClearSessionCookie must always agree on the cookie's Path (basePath
// when set, else "/"), or logout leaves the login-set cookie behind under a
// reverse-proxy prefix.
func TestSessionCookiePathUnderBasePath(t *testing.T) {
	sess := &model.Session{ID: "abc123", ExpiresAt: time.Now().Add(time.Hour)}

	tests := []struct {
		name     string
		basePath string
		wantPath string
	}{
		{name: "unset base path defaults to root", basePath: "", wantPath: "/"},
		{name: "base path used verbatim", basePath: "/x/y", wantPath: "/x/y"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRec := httptest.NewRecorder()
			SetSessionCookie(setRec, sess, false, tt.basePath)
			setCookies := setRec.Result().Cookies()
			if len(setCookies) != 1 {
				t.Fatalf("SetSessionCookie: expected 1 cookie, got %d", len(setCookies))
			}
			if got := setCookies[0].Path; got != tt.wantPath {
				t.Fatalf("SetSessionCookie Path = %q, want %q", got, tt.wantPath)
			}

			clearRec := httptest.NewRecorder()
			ClearSessionCookie(clearRec, tt.basePath)
			clearCookies := clearRec.Result().Cookies()
			if len(clearCookies) != 1 {
				t.Fatalf("ClearSessionCookie: expected 1 cookie, got %d", len(clearCookies))
			}
			if got := clearCookies[0].Path; got != tt.wantPath {
				t.Fatalf("ClearSessionCookie Path = %q, want %q", got, tt.wantPath)
			}
			if setCookies[0].Path != clearCookies[0].Path {
				t.Fatalf("Set/Clear Path mismatch: %q vs %q", setCookies[0].Path, clearCookies[0].Path)
			}
		})
	}
}
