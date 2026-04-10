package middleware

import (
	"net/http"
	"strings"
)

// maxMethodOverrideBytes is the upper bound for body reads when scanning for
// the _method field. This prevents memory exhaustion (gosec G120).
const maxMethodOverrideBytes = 1024

// MethodOverride returns HTTP middleware that reads a hidden "_method" form
// field on POST requests and replaces r.Method with its upper-cased value.
// This enables HTML forms — which only support GET and POST — to simulate
// PUT, PATCH, and DELETE.
//
// Only PUT, PATCH, and DELETE are accepted; any other value in _method is
// silently ignored to prevent rewriting to unexpected methods such as TRACE
// or CONNECT.
//
// Apply this middleware only to auth-protected routes. Never apply it to
// /ping/* endpoints which must not have their request method rewritten.
func MethodOverride(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, maxMethodOverrideBytes)
			if method := r.FormValue("_method"); method != "" {
				switch method = strings.ToUpper(method); method {
				case http.MethodPut, http.MethodPatch, http.MethodDelete:
					r.Method = method
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
