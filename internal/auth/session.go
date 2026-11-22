package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

// TokenDigest is the persistent lookup form of an opaque browser credential.
func TokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

// CSRFToken derives a session-bound mutation token from the HttpOnly cookie.
// It is reproducible for session reads without storing a raw token in the database.
func CSRFToken(sessionToken string) string {
	mac := hmac.New(sha256.New, []byte(sessionToken))
	mac.Write([]byte("shiftbox/csrf/v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ValidCSRF compares the stored binding in constant time.
func ValidCSRF(session Session, token string) bool {
	return token != "" && subtle.ConstantTimeCompare([]byte(session.CSRFDigest), []byte(TokenDigest(token))) == 1
}

// NewSession returns a random cookie once; callers persist only the returned record.
func NewSession(id, userID, mode string, now time.Time, lifetime time.Duration) (Session, string, error) {
	if id == "" || userID == "" || lifetime <= 0 {
		return Session{}, "", errors.New("invalid session identity or lifetime")
	}
	switch mode {
	case "normal", "bootstrap_restricted", "password_change", "recovery_review":
	default:
		return Session{}, "", errors.New("invalid session mode")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return Session{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	return Session{ID: id, UserID: userID, Mode: mode, TokenDigest: TokenDigest(token), CSRFDigest: TokenDigest(CSRFToken(token)), AuthenticatedAt: now.UTC(), ExpiresAt: now.Add(lifetime).UTC()}, token, nil
}
