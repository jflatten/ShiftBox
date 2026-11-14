package web

import (
	"encoding/base64"
	"errors"
	"net/http"
	"time"
)

const _sessionCookieName = "__Host-shiftbox_session"

// SetSessionCookie issues only the management host's opaque session credential.
// The caller must commit the session before handing its cookie to the browser.
func SetSessionCookie(w http.ResponseWriter, token string, expires time.Time) error {
	if !validSessionToken(token) || expires.IsZero() {
		return errors.New("invalid session cookie")
	}
	cookie := managementCookie(token)
	cookie.Expires = expires.UTC()
	http.SetCookie(w, &cookie)
	return nil
}

// SessionToken accepts exactly one credential in the controller-issued format.
// Resolving this token still requires current membership and session checks.
func SessionToken(r *http.Request) (string, error) {
	cookies := r.CookiesNamed(_sessionCookieName)
	if len(cookies) != 1 || !validSessionToken(cookies[0].Value) {
		return "", Problem{Status: 401, Code: "auth.required", Title: "Sign in to continue"}
	}
	return cookies[0].Value, nil
}

// ClearSessionCookie expires the same host-only scope used when signing in.
// Durable revocation and live connection closure must happen before logout succeeds.
func ClearSessionCookie(w http.ResponseWriter) {
	cookie := managementCookie("")
	cookie.MaxAge = -1
	cookie.Expires = time.Unix(1, 0).UTC()
	http.SetCookie(w, &cookie)
}

func managementCookie(token string) http.Cookie {
	return http.Cookie{Name: _sessionCookieName, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}
}

func validSessionToken(token string) bool {
	if len(token) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	return err == nil && len(decoded) == 32
}
