package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"unicode/utf8"

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
