package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// NetworkPolicy describes an administrator-selected outbound access policy.
type NetworkPolicy struct {
	Name  string        `json:"name"`
	Mode  string        `json:"mode"`
	Rules []NetworkRule `json:"rules,omitempty"`
}

// NetworkRule describes a destination CIDR, transport and port.
type NetworkRule struct {
	Destination string `json:"destination"`
	Transport   string `json:"transport"`
	Port        int    `json:"port"`
}

var (
	// ErrNetworkPolicyInvalid indicates that the requested policy is malformed.
	ErrNetworkPolicyInvalid = errors.New("invalid network policy")
	// ErrNetworkPolicyConflict indicates a duplicate policy name.
	ErrNetworkPolicyConflict = errors.New("network policy already exists")
)

// CreateNetworkPolicyInTx records only policies the current adapter can enforce.
func CreateNetworkPolicyInTx(ctx context.Context, tx *sql.Tx, id string, policy NetworkPolicy, caps provider.Capabilities) error {
	if !policy.valid() {
		return ErrNetworkPolicyInvalid
	}
	if policy.Mode != "all" || !slices.Contains(caps.NetworkModes, "shared") || caps.Version == "" {
		return provider.ErrCapabilityUnsupported
	}
	// Shared networking leaves outbound traffic available, so it requires no exceptions.
	result, err := tx.ExecContext(ctx, `INSERT INTO network_policies(id,name,mode,rules_json,required_exceptions_json,provider_capability_version)
		VALUES($1,$2,$3,'[]','[]',$4) ON CONFLICT DO NOTHING`, id, policy.Name, policy.Mode, caps.Version)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrNetworkPolicyConflict
	}
	return err
}

func (p NetworkPolicy) valid() bool {
	if strings.TrimSpace(p.Name) == "" || utf8.RuneCountInString(p.Name) > 120 || strings.ContainsRune(p.Name, 0) {
		return false
	}
	switch p.Mode {
	case "all", "no_network":
		return len(p.Rules) == 0
	case "allowlist":
		for _, rule := range p.Rules {
			if _, err := netip.ParsePrefix(rule.Destination); err != nil ||
				(rule.Transport != "tcp" && rule.Transport != "udp") || rule.Port < 1 || rule.Port > 65535 {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// LaunchNetworkSnapshot pins the admitted policy and its effective provider mode.
func LaunchNetworkSnapshot(ctx context.Context, tx *sql.Tx, id string, caps provider.Capabilities) (string, error) {
	if !slices.Contains(caps.NetworkModes, "shared") {
		return "", provider.ErrCapabilityUnsupported
	}
	if id == "" {
		return `{"mode":"shared"}`, nil
	}
	var version string
	err := tx.QueryRowContext(ctx, `SELECT provider_capability_version FROM network_policies
		WHERE id=$1 AND enabled=1 AND mode='all' AND rules_json='[]' AND required_exceptions_json='[]'`, id).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && version != caps.Version) {
		return "", provider.ErrCapabilityUnsupported
	}
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(struct {
		Mode     string `json:"mode"`
		PolicyID string `json:"policyId"`
	}{"shared", id})
	return string(encoded), err
}

// VerifyStoppedNetwork verifies the exact configured VM while the caller holds
// the lifecycle admission lock. The MVP shared policy only observes UTM.
func VerifyStoppedNetwork(ctx context.Context, tx *sql.Tx, id string, generation int64, snapshot string,
	verify func(context.Context, provider.NetworkRequest) (provider.Inventory, error),
) error {
	mode, err := effectiveNetworkMode(snapshot)
	if err != nil || verify == nil {
		return provider.ErrOutcomeUnknown
	}
	ref := provider.WorkspaceRef{WorkspaceID: id, Generation: generation}
	var artifact, release string
	err = tx.QueryRowContext(ctx, `SELECT w.installation_id,w.ownership_marker,COALESCE(w.provider_instance_id,''),w.platform_artifact_id,a.release_id
		FROM workspaces w JOIN platform_artifacts a ON a.id=w.platform_artifact_id
		WHERE w.id=$1 AND w.generation=$2 AND w.desired_state='stopped' AND w.observed_state='stopped'`, id, generation).
		Scan(&ref.InstallationID, &ref.OwnershipMarker, &ref.InstanceID, &artifact, &release)
	if errors.Is(err, sql.ErrNoRows) || err == nil && ref.InstanceID == "" {
		return provider.ErrOutcomeUnknown
	}
	if err != nil {
		return err
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	got, err := verify(probe, provider.NetworkRequest{Ref: ref, OperationID: uuid.NewString(), Mode: mode})
	if err != nil || got.Ref != ref || got.InstanceID != ref.InstanceID || got.Power != provider.Stopped ||
		got.NetworkMode != mode || got.ArtifactID != artifact || got.Release != release || got.ObservedAt.IsZero() {
		return provider.ErrOutcomeUnknown
	}
	return nil
}
