package identity_test

import (
	"context"
	"errors"
	api "github.com/dexidp/dex/api/v2"
	"github.com/jflatten/ShiftBox/internal/identity"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"testing"
)

type passwordAPI struct {
	existing *api.Password
	creates  int
	failure  error
}

func (f *passwordAPI) CreatePassword(ctx context.Context, r *api.CreatePasswordReq, opts ...grpc.CallOption) (*api.CreatePasswordResp, error) {
	f.creates++
	if f.failure != nil {
		return nil, f.failure
	}
	if f.existing != nil {
		return &api.CreatePasswordResp{AlreadyExists: true}, nil
	}
	f.existing = r.Password
	return &api.CreatePasswordResp{}, nil
}
func (f *passwordAPI) ListPasswords(context.Context, *api.ListPasswordReq, ...grpc.CallOption) (*api.ListPasswordResp, error) {
	return &api.ListPasswordResp{Passwords: []*api.Password{f.existing}}, f.failure
}

// TestEnsureLocalPasswordPreservesChangedCredential prevents retried bootstrap from resetting the owner's changed password or claiming another identity.
func TestEnsureLocalPasswordPreservesChangedCredential(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("temporary-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	fake := &passwordAPI{}
	admin := identity.PasswordAdmin{Client: fake}
	if err := admin.Ensure(context.Background(), "owner@example.test", "owner-id", hash); err != nil {
		t.Fatal(err)
	}
	changed, err := bcrypt.GenerateFromPassword([]byte("changed-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	fake.existing.Hash = changed
	if err := admin.Ensure(context.Background(), "owner@example.test", "owner-id", hash); err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword(fake.existing.Hash, []byte("changed-password")); err != nil {
		t.Fatal("bootstrap retry reset changed credential")
	}
	if err := admin.Ensure(context.Background(), "owner@example.test", "another-id", hash); !errors.Is(err, identity.ErrLocalIdentityConflict) {
		t.Errorf("identity collision = %v, want conflict", err)
	}
	fake.failure = errors.New("private database credential")
	if err := admin.Ensure(context.Background(), "owner@example.test", "owner-id", hash); !errors.Is(err, identity.ErrPasswordAdministration) {
		t.Errorf("Dex outage = %v, want safe administration error", err)
	}
}

// TestTemporaryPasswordIsUniqueAndHashed ensures generated bootstrap credentials are independent and the persisted value verifies without storing plaintext.
func TestTemporaryPasswordIsUniqueAndHashed(t *testing.T) {
	first, hash, err := identity.NewTemporaryPassword()
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := identity.NewTemporaryPassword()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) < 32 {
		t.Fatal("temporary passwords lack independent high entropy")
	}
	if err := bcrypt.CompareHashAndPassword(hash, []byte(first)); err != nil {
		t.Fatal("generated hash does not verify")
	}
}
