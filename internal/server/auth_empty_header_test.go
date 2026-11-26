package server

import (
	"net/http"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// A credential header that is present but empty after the "Bearer" scheme is
// refused with 401, with and without anonymous access: it is a broken
// credential, never "no credential" (which becomes anonymous with
// --allow-anonymous). A request without such a header is unchanged.
func TestEmptyCredentialHeader_Refused(t *testing.T) {
	st := testAuthStore(t)
	empty := []struct {
		name    string
		headers http.Header
	}{
		{"empty Authorization", http.Header{"Authorization": {""}}},
		{"blank Authorization", http.Header{"Authorization": {"   "}}},
		{"Bearer without a token", http.Header{"Authorization": {"Bearer"}}},
		{"Bearer and a space", http.Header{"Authorization": {"Bearer "}}},
		{"Bearer and spaces", http.Header{"Authorization": {"Bearer    "}}},
		{"empty X-MG-RAST-Token", http.Header{"X-Mg-Rast-Token": {""}}},
		{"blank X-MG-RAST-Token", http.Header{"X-Mg-Rast-Token": {"  "}}},
	}
	for _, mode := range []struct {
		name string
		anon *AnonymousConfig
	}{
		{"anonymous off", nil},
		{"anonymous on", &AnonymousConfig{Enabled: true}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			for _, c := range empty {
				t.Run(c.name, func(t *testing.T) {
					h, got := authProbe(st, authProbeOpts{anon: mode.anon})
					rec, env := probeHeaders(t, h, c.headers)
					wantInvalidToken(t, rec, env)
					if got() != nil {
						t.Errorf("the request reached the handler as %+v", got().User)
					}
				})
			}

			// No credential header at all: anonymous when allowed, else
			// "authentication required", as before.
			h, got := authProbe(st, authProbeOpts{anon: mode.anon})
			rec, env := probeHeaders(t, h, http.Header{})
			if mode.anon == nil {
				if rec.Code != http.StatusUnauthorized || env.Error == nil || env.Error.Message != "authentication required" {
					t.Errorf("no header, anonymous off: %d %+v, want 401 authentication required", rec.Code, env.Error)
				}
				return
			}
			if rec.Code != http.StatusOK || got() == nil || got().User == nil || got().User.Username != model.AnonymousUser.Username {
				t.Errorf("no header, anonymous on: %d, context %+v; want the anonymous user", rec.Code, got())
			}
		})
	}
}
