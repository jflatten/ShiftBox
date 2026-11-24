package app

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"sync"

	"github.com/jflatten/ShiftBox/internal/config"
	"github.com/jflatten/ShiftBox/internal/web"
)

// PrivateSetupClaim holds only the digest of the deployment's first-owner code.
type PrivateSetupClaim struct {
	digest [sha256.Size]byte
	mu     sync.Mutex
}

// NewPrivateSetupClaim requires a deployment-generated, high-entropy claim.
func NewPrivateSetupClaim(code string) (*PrivateSetupClaim, error) {
	if len(code) < 32 {
		return nil, errors.New("private setup requires a deployment claim code")
	}
	return &PrivateSetupClaim{digest: sha256.Sum256([]byte(code))}, nil
}

func (claim *PrivateSetupClaim) accepts(code string) bool {
	digest := sha256.Sum256([]byte(code))
	return subtle.ConstantTimeCompare(claim.digest[:], digest[:]) == 1
}

// Only the private management listener may use this guard. All mutations
// still require the first-owner claim or a restricted session and exact Origin.
func protectPrivateSetup(origin string, cloud *CloudSetup, next http.Handler) (http.Handler, error) {
	hosts, err := config.NewServiceHosts(origin, cloud.IDEHostPattern, cloud.PreviewHostPattern)
	if err != nil || origin != cloud.ManagementOrigin {
		return nil, errors.New("private setup requires the configured management HTTPS origin")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS == nil || r.Host != hosts.ManagementAuthority() {
			web.WriteProblem(w, web.RequestID(r.Context()), web.Problem{Status: 403,
				Code: "setup.private_access_required", Title: "Open the private ShiftBox HTTPS URL"})
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}
