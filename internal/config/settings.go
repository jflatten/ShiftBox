package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrProfileImmutable requires an established installation to retain its database
// profile. Guarded restore is a separate flow, never an ordinary settings update.
var ErrProfileImmutable = errors.New("active installation data profile is immutable")

// InstallationSettings is the non-secret installation portion of an activation.
// Connection credentials and certificate keys belong in protected storage.
type InstallationSettings struct {
	BrowserCertificateID        string `json:"browserCertificateId,omitempty"`
	OrganizationName            string `json:"organizationName"`
	DataProfile                 string `json:"dataProfile"`
	ManagementOrigin            string `json:"managementOrigin"`
	IDEHostPattern              string `json:"ideHostPattern"`
	PreviewHostPattern          string `json:"previewHostPattern"`
	ExternalAuthIntervalSeconds int64  `json:"externalAuthIntervalSeconds"`
}

// RecordInstallationSettings validates and records installation settings together
// with their generation snapshot. The caller must roll back on any error and
// validate certificate/database prerequisites before committing activation.
// This function does not activate listeners, change authority, or bypass restore.
func RecordInstallationSettings(ctx context.Context, tx *sql.Tx, activation Activation, settings InstallationSettings) (int64, error) {
	if strings.TrimSpace(settings.OrganizationName) == "" || (settings.DataProfile != "sqlite" && settings.DataProfile != "postgres") || settings.ExternalAuthIntervalSeconds <= 0 || settings.ExternalAuthIntervalSeconds > int64(time.Duration(1<<63-1)/time.Second) {
		return 0, errors.New("invalid installation settings")
	}
	if _, err := NewServiceHosts(settings.ManagementOrigin, settings.IDEHostPattern, settings.PreviewHostPattern); err != nil {
		return 0, err
	}
	var profile, state string
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT data_profile,state,config_generation FROM installations WHERE id=$1`, activation.InstallationID).Scan(&profile, &state, &generation); err != nil {
		return 0, err
	}
	if generation != activation.ExpectedGeneration {
		return 0, ErrActivationConflict
	}
	if (generation > 0 || state == "active") && profile != settings.DataProfile {
		return 0, ErrProfileImmutable
	}
	snapshot, err := json.Marshal(settings)
	if err != nil {
		return 0, err
	}
	// Derive the snapshot from the same typed settings used for the live row;
	// caller-supplied JSON cannot record different settings or raw credentials.
	activation.SettingsJSON = string(snapshot)
	next, err := RecordActivation(ctx, tx, activation)
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE installations SET organization_name=$1,data_profile=$2,management_origin=$3,ide_host_pattern=$4,preview_host_pattern=$5,external_auth_interval_seconds=$6 WHERE id=$7`, settings.OrganizationName, settings.DataProfile, settings.ManagementOrigin, settings.IDEHostPattern, settings.PreviewHostPattern, settings.ExternalAuthIntervalSeconds, activation.InstallationID)
	if err != nil {
		return 0, err
	}
	return next, nil
}
