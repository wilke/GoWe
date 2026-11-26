package ui

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A username in the reserved service realm is refused before the password
// is sent to BV-BRC, and no session is created.
func TestHandleLoginPost_RefusesServiceRealm(t *testing.T) {
	for _, username := range []string{"model-router@service.gowe", "X@Service.Gowe"} {
		t.Run(username, func(t *testing.T) {
			st := setupTestStore(t)
			defer st.Close()
			var logs bytes.Buffer
			u := New(st, slog.New(slog.NewTextHandler(&logs, nil)), Config{})

			form := url.Values{"username": {username}, "password": {"secret"}}
			req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			u.HandleLoginPost(rec, req)

			if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "error=Invalid+credentials") {
				t.Fatalf("status %d, Location %q; want 303 to the login error", rec.Code, rec.Header().Get("Location"))
			}
			for _, c := range rec.Result().Cookies() {
				if c.Name == SessionCookieName && c.Value != "" {
					t.Errorf("a session cookie was set: %+v", c)
				}
			}
			// Refused by the realm check, not by a failed BV-BRC call.
			if !strings.Contains(logs.String(), "reserved service realm") || strings.Contains(logs.String(), "login failed") {
				t.Errorf("log does not show the realm refusal before any BV-BRC call:\n%s", logs.String())
			}
		})
	}
}
