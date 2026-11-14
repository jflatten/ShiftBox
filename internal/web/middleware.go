package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jflatten/ShiftBox/internal/auth"
)

type requestIDKey struct{}

// RequestID returns the controller-generated correlation ID for this request.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// WithRequestID replaces incoming identifiers so diagnostics never trust guest input.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var value [16]byte
		if _, err := rand.Read(value[:]); err != nil {
			WriteProblem(w, "", err)
			return
		}
		value[6] = (value[6] & 0x0f) | 0x40
		value[8] = (value[8] & 0x3f) | 0x80
		id := fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// ValidUUID validates the opaque UUID representation used by route identifiers.
func ValidUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil && len(decoded) == 16
}

// DecodeJSON bounds input, rejects unknown fields, and accepts exactly one document.
// Parser errors are deliberately not returned because they can quote protected input.
func DecodeJSON(w http.ResponseWriter, r *http.Request, destination any, limit int64) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(destination)
	if err == nil {
		next := decoder.Decode(new(any))
		switch next {
		case io.EOF:
		case nil:
			err = errors.New("trailing document")
		default:
			err = next
		}
	}
	if err == nil {
		return nil
	}
	var oversized *http.MaxBytesError
	if errors.As(err, &oversized) {
		return Problem{Status: 413, Code: "request.too_large", Title: "Request is too large"}
	}
	return Problem{Status: 400, Code: "request.invalid_body", Title: "Check the submitted fields"}
}

// RequireVersion accepts a single strong version precondition, including a stale zero.
func RequireVersion(r *http.Request) (int64, error) {
	values := r.Header.Values("If-Match")
	if len(values) == 0 || values[0] == "" {
		return 0, Problem{Status: 428, Code: "request.precondition_required", Title: "Refresh before making this change"}
	}
	value := values[0]
	invalid := Problem{Status: 400, Code: "request.invalid_version", Title: "Invalid resource version"}
	if len(values) != 1 || len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, invalid
	}
	for _, character := range value[1 : len(value)-1] {
		if character < '0' || character > '9' {
			return 0, invalid
		}
	}
	version, err := strconv.ParseInt(value[1:len(value)-1], 10, 64)
	if err != nil {
		return 0, invalid
	}
	return version, nil
}

// IdempotencyKey validates the request identity before durable operation creation.
func IdempotencyKey(r *http.Request) (string, error) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || len(values[0]) < 16 || len(values[0]) > 128 {
		return "", Problem{Status: 400, Code: "request.invalid_idempotency_key", Title: "A valid request identifier is required"}
	}
	return values[0], nil
}

// MutationGuard supplements authentication with exact Origin and session-bound CSRF.
// The authorization layer must separately validate current membership and session gates.
func MutationGuard(origin string, session func(*http.Request) (auth.Session, error), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		current, err := session(r)
		if err != nil {
			WriteProblem(w, RequestID(r.Context()), Problem{Status: 401, Code: "auth.required", Title: "Sign in to continue"})
			return
		}
		if err := checkMutation(r, origin, current); err != nil {
			WriteProblem(w, RequestID(r.Context()), err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func checkMutation(r *http.Request, origin string, session auth.Session) error {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return nil
	}
	origins := r.Header.Values("Origin")
	tokens := r.Header.Values("X-CSRF-Token")
	if len(origins) != 1 || origins[0] != origin || len(tokens) != 1 || !auth.ValidCSRF(session, tokens[0]) {
		return Problem{Status: 403, Code: "auth.invalid_origin_or_csrf", Title: "This request is not authorized", Detail: "Return to ShiftBox and try again."}
	}
	return nil
}
