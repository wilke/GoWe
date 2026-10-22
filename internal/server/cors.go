package server

import "net/http"

// corsAllowMethods lists the HTTP methods exposed anywhere under /api/v1.
// Sent verbatim on every preflight response since CORS has no per-route
// granularity here — the flag is an all-or-nothing opt-in for the whole API.
const corsAllowMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"

// corsAllowHeaders lists the request headers browser clients are permitted
// to send cross-origin: the bearer token and JSON request bodies.
const corsAllowHeaders = "Authorization, Content-Type"

// corsMaxAge is how long (seconds) a browser may cache a preflight response
// before re-checking it.
const corsMaxAge = "600"

// corsMiddleware answers CORS preflight and annotates normal responses for a
// fixed allowlist of exact origins. It is only mounted when --cors-origins is
// non-empty (see routes()); with the flag unset, /api/v1 behaves exactly as
// it always has — no Access-Control-* headers, and OPTIONS 405s via chi's
// default method-not-allowed handling.
//
// Unlisted origins (including requests with no Origin header at all, e.g.
// curl or same-origin fetches) receive no CORS headers and fall through to
// the normal handler chain unchanged — an OPTIONS request from an unlisted
// origin therefore still 405s, exactly like today.
//
// The origin is echoed verbatim (never "*"): the API accepts bearer tokens
// via Authorization, and a wildcard origin is unsafe to pair with that.
func corsMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" || !allowed[origin] {
				next.ServeHTTP(w, r)
				return
			}

			// Allow-Origin varies per request depending on the Origin header,
			// so caches must not conflate responses across origins.
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")

			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", corsAllowMethods)
				w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
				w.Header().Set("Access-Control-Max-Age", corsMaxAge)
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
