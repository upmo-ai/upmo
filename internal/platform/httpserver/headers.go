package httpserver

import (
	"net/http"
	"strings"
)

// hsts is the Strict-Transport-Security value: about six months, no
// includeSubDomains and no preload, so a deployment cannot lock a whole domain
// to HTTPS by accident.
const hsts = "max-age=15552000"

// APIHeaders returns the security headers of a JSON API response. A secure
// request also gets Strict-Transport-Security; on plain HTTP it is left out,
// because a browser ignores it there and a proxy would only pass on a lie.
func APIHeaders(secure bool) http.Header {
	h := http.Header{}
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
	if secure {
		h.Set("Strict-Transport-Security", hsts)
	}
	return h
}

// SecurityHeaders sets APIHeaders on every response before next writes, keyed
// off whether the connection itself is TLS. A path under /api/ whose handler
// leaves Cache-Control empty is marked no-store; a handler that sets its own
// value, such as a stream, keeps it.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, values := range APIHeaders(r.TLS != nil) {
			w.Header()[name] = values
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
