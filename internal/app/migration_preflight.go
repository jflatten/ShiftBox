package app

import (
	"context"
	"time"

	"github.com/jflatten/ShiftBox/internal/release"
)

// MigrationEvidence checks retained guest observations and the active
// installation materials loaded by controller preflight.
func (s *ControllerState) MigrationEvidence(ctx context.Context, evidence release.MigrationEvidence, protocol string, now time.Time) (release.MigrationEvidence, error) {
	guests, err := release.NewStore(s.DB).GuestUpgradeBlockers(ctx, s.Installation.InstallationID, s.Installation.ActiveRelease, protocol)
	if err != nil {
		return release.MigrationEvidence{}, err
	}
	evidence.Guests = guests
	evidence.DatabaseOwned = true
	evidence.KeyAccessible = s.cipher != nil
	browser, err := s.browserCertificateAt(now)
	evidence.BrowserValid = err == nil && browser.Leaf != nil
	if s.GuestCA != nil && s.GuestCA.Certificate != nil {
		evidence.GuestCAUntil = s.GuestCA.Certificate.NotAfter
	}
	return evidence, nil
}
