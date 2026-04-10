package middleware

import (
	"net/http"
	"strings"
)

// MethodOverride returns HTTP middleware that reads a hidden "_method" form
// field on POST requests and replaces r.Method with its upper-cased value.
// This enables HTML forms — which only support GET and POST — to simulate
// PUT, PATCH, and DELETE.
//
// Apply this middleware only to auth-protected routes. Never apply it to
// /ping/* endpoints which must not have their request method rewritten.
func MethodOverride(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if method := r.FormValue("_method"); method != "" {
				r.Method = strings.ToUpper(method)
			}
		}
		next.ServeHTTP(w, r)
	})
}
