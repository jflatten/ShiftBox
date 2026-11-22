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

// PasswordVerified checks the current credential over the authenticated Dex API.
func (a PasswordAdmin) PasswordVerified(ctx context.Context, email, password string) (bool, error) {
	client, ok := a.Client.(interface {
		VerifyPassword(context.Context, *api.VerifyPasswordReq, ...grpc.CallOption) (*api.VerifyPasswordResp, error)
	})
	if !ok {
		return false, ErrPasswordAdministration
	}
	result, err := client.VerifyPassword(ctx, &api.VerifyPasswordReq{Email: email, Password: password})
	if err != nil || result == nil || result.NotFound {
		return false, ErrPasswordAdministration
	}
	return result.Verified, nil
}

// ReplacePassword stores a bcrypt hash in Dex for the existing local account.
func (a PasswordAdmin) ReplacePassword(ctx context.Context, email, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return ErrPasswordAdministration
	}
	return a.replaceHash(ctx, email, hash)
}

// ResetPasswordHash replaces a credential only for the expected Dex subject.
// The caller serializes password writes for the installation.
func (a PasswordAdmin) ResetPasswordHash(ctx context.Context, email, userID string, hash []byte) error {
	if a.Client == nil || email == "" || userID == "" {
		return ErrPasswordAdministration
	}
	if _, err := bcrypt.Cost(hash); err != nil {
		return ErrPasswordAdministration
	}
	result, err := a.Client.ListPasswords(ctx, &api.ListPasswordReq{})
	if err != nil || result == nil {
		return ErrPasswordAdministration
	}
	for _, password := range result.Passwords {
		if password != nil && password.Email == email && password.UserId == userID {
			return a.replaceHash(ctx, email, hash)
		}
	}
	return ErrLocalIdentityConflict
}

func (a PasswordAdmin) replaceHash(ctx context.Context, email string, hash []byte) error {
	client, ok := a.Client.(interface {
		UpdatePassword(context.Context, *api.UpdatePasswordReq, ...grpc.CallOption) (*api.UpdatePasswordResp, error)
	})
	if !ok {
		return ErrPasswordAdministration
	}
	result, err := client.UpdatePassword(ctx, &api.UpdatePasswordReq{Email: email, NewHash: hash})
	if err != nil || result == nil || result.NotFound {
		return ErrPasswordAdministration
	}
	return nil
}
