// Package middleware provides HTTP middleware for CronMon.
package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
)

// BasicAuth returns middleware that enforces HTTP Basic Authentication.
//
// Both the submitted and expected credentials are hashed with SHA-256 before
// comparison so that subtle.ConstantTimeCompare always operates on equal-length
// inputs, eliminating the length-based timing oracle that affects raw-string
// comparison. Both fields are always compared regardless of whether the
// username matches, preventing a username-existence timing side-channel.
//
// On failure the handler writes a 401 with a WWW-Authenticate challenge header
// and does not call the next handler.
func BasicAuth(username, password string) func(http.Handler) http.Handler {
	wantUser := sha256.Sum256([]byte(username))
	wantPass := sha256.Sum256([]byte(password))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, pass, ok := r.BasicAuth()

			// Hash submitted credentials so both slices are always 32 bytes,
			// ensuring ConstantTimeCompare cannot short-circuit on length.
			gotUser := sha256.Sum256([]byte(user))
			gotPass := sha256.Sum256([]byte(pass))

			userMatch := subtle.ConstantTimeCompare(gotUser[:], wantUser[:])
			passMatch := subtle.ConstantTimeCompare(gotPass[:], wantPass[:])

			if !ok || (userMatch&passMatch) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="CronMon"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
