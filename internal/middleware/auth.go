// Package middleware provides HTTP middleware for CronMon.
package middleware

import (
	"crypto/subtle"
	"net/http"
)

// BasicAuth returns middleware that enforces HTTP Basic Authentication.
//
// It uses crypto/subtle.ConstantTimeCompare for both the username and password
// comparisons — and crucially compares both regardless of whether the username
// matches — to prevent timing oracle attacks that could leak the username.
//
// On failure the handler writes a 401 with a WWW-Authenticate challenge header
// and does not call the next handler.
func BasicAuth(username, password string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, pass, ok := r.BasicAuth()

			// Always compare both fields to prevent timing side-channels.
			// ConstantTimeCompare returns 1 on equality; combining with & means
			// both must match for the result to be 1.
			userMatch := subtle.ConstantTimeCompare([]byte(user), []byte(username))
			passMatch := subtle.ConstantTimeCompare([]byte(pass), []byte(password))

			if !ok || (userMatch&passMatch) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="CronMon"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
