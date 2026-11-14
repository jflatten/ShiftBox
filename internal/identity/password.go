package identity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/mail"
	"strings"

	api "github.com/dexidp/dex/api/v2"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
)

var (
	// ErrLocalIdentityConflict requires operator review rather than account reassignment.
	ErrLocalIdentityConflict = errors.New("local identity already belongs to a different user")
	// ErrPasswordAdministration omits Dex/transport diagnostics that may contain credentials.
	ErrPasswordAdministration = errors.New("local password administration is unavailable")
)

// PasswordAPI is the subset of Dex's supported API needed for resumable creation.
// Composition must supply a private loopback client authenticated with dedicated
// administrator mTLS credentials, never guest certificates or plaintext gRPC.
type PasswordAPI interface {
	CreatePassword(context.Context, *api.CreatePasswordReq, ...grpc.CallOption) (*api.CreatePasswordResp, error)
	ListPasswords(context.Context, *api.ListPasswordReq, ...grpc.CallOption) (*api.ListPasswordResp, error)
}

// PasswordAdmin preserves the stable local identity across interrupted setup.
type PasswordAdmin struct{ Client PasswordAPI }

// Ensure creates a credential or verifies the existing email-to-user binding.
// It never updates an existing hash, even when the caller retries an old intent.
// Call outside database transactions after committing the intended user identity.
func (a PasswordAdmin) Ensure(ctx context.Context, email, userID string, hash []byte) error {
	email = strings.ToLower(strings.TrimSpace(email))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || userID == "" || a.Client == nil {
		return ErrPasswordAdministration
	}
	if _, err := bcrypt.Cost(hash); err != nil {
		return ErrPasswordAdministration
	}
	response, err := a.Client.CreatePassword(ctx, &api.CreatePasswordReq{Password: &api.Password{Email: email, UserId: userID, Hash: append([]byte(nil), hash...)}})
	if err != nil || response == nil {
		return ErrPasswordAdministration
	}
	if !response.AlreadyExists {
		return nil
	}
	existing, err := a.Client.ListPasswords(ctx, &api.ListPasswordReq{})
	if err != nil || existing == nil {
		return ErrPasswordAdministration
	}
	for _, password := range existing.Passwords {
		if password == nil || password.Email != email {
			continue
		}
		if password.UserId != userID {
			return ErrLocalIdentityConflict
		}
		return nil
	}
	return ErrPasswordAdministration
}

// NewTemporaryPassword returns one high-entropy local bootstrap credential and
// its Dex-compatible hash. Only the protected one-time output may receive the
// plaintext; durable setup state stores the hash and delivery state separately.
func NewTemporaryPassword() (string, []byte, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", nil, ErrPasswordAdministration
	}
	password := base64.RawURLEncoding.EncodeToString(random[:])
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, ErrPasswordAdministration
	}
	return password, hash, nil
}
