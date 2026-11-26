package server

import (
	"context"
	"net"
	"net/http"
)

// ctxKeyPeerAddr holds r.RemoteAddr as the connection gave it, before
// middleware.RealIP rewrites it from client-supplied headers.
const ctxKeyPeerAddr ctxKey = "peer_addr"

// maxPeerAddrBytes bounds a recorded peer address.
const maxPeerAddrBytes = 64

// peerAddrMiddleware stores the request's RemoteAddr in the context. It must
// run before middleware.RealIP, which replaces RemoteAddr with the value of
// True-Client-IP, X-Real-IP or X-Forwarded-For, headers any client can set.
func peerAddrMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), ctxKeyPeerAddr, r.RemoteAddr)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// peerAddr returns the host part of the remote address of r's connection as
// recorded by peerAddrMiddleware, or the whole recorded address when it has
// no port; reduced to printable ASCII and at most maxPeerAddrBytes. It is ""
// (unknown) when the middleware did not run (RemoteAddr may have been
// rewritten from headers by then) and when the address is empty or "@" (a
// unix-socket listener). Registration stores "" as is; a heartbeat with ""
// keeps the stored value (TouchWorker). Behind a reverse proxy this is the
// proxy's address; it is never taken from forwarding headers, whatever
// --behind-proxy says.
func peerAddr(r *http.Request) string {
	raw, _ := r.Context().Value(ctxKeyPeerAddr).(string)
	if raw == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	addr := truncateUTF8(printableASCII(raw), maxPeerAddrBytes)
	if addr == "@" {
		return ""
	}
	return addr
}
