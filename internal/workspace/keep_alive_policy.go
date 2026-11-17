package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// KeepAlivePolicy defines the administrator's inactivity shutdown choices.
// Zero minutes means Unlimited; a nil workspace override inherits the default.
type KeepAlivePolicy struct {
	MaximumMinutes int64   `json:"maximumMinutes"`
	AllowedMinutes []int64 `json:"allowedMinutes"`
	DefaultMinutes int64   `json:"defaultMinutes"`
	Version        int64   `json:"version"`
}

// Validate rejects ambiguous choices and durations outside the controller's
// supported clock range. A finite maximum cannot offer Unlimited.
func (p KeepAlivePolicy) Validate() error {
	const maximumMinutes = int64((1<<63 - 1) / time.Minute)
	if p.MaximumMinutes < 0 || p.MaximumMinutes > maximumMinutes || len(p.AllowedMinutes) == 0 || len(p.AllowedMinutes) > 64 {
		return errors.New("invalid keep-alive policy")
	}
	seen := make(map[int64]bool, len(p.AllowedMinutes))
	for _, minutes := range p.AllowedMinutes {
		if minutes < 0 || minutes > maximumMinutes || seen[minutes] || (p.MaximumMinutes > 0 && (minutes == 0 || minutes > p.MaximumMinutes)) {
			return errors.New("invalid keep-alive choices")
		}
		seen[minutes] = true
	}
	if !seen[p.DefaultMinutes] {
		return errors.New("keep-alive default is not an allowed choice")
	}
	return nil
}

// EffectiveMinutes resolves an override against the current policy. Removing an
// option or lowering the maximum makes affected workspaces inherit the default.
func (p KeepAlivePolicy) EffectiveMinutes(override *int64) int64 {
	if override != nil && slices.Contains(p.AllowedMinutes, *override) &&
		(p.MaximumMinutes == 0 || (*override > 0 && *override <= p.MaximumMinutes)) {
		return *override
	}
	return p.DefaultMinutes
}

// ReadKeepAlivePolicy loads current administrator settings inside the caller's
// database transaction when supplied one, avoiding cached policy decisions.
func ReadKeepAlivePolicy(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (KeepAlivePolicy, error) {
	var policy KeepAlivePolicy
	var encoded string
	err := query.QueryRowContext(ctx, `SELECT keep_alive_maximum_minutes,keep_alive_allowed_minutes_json,
		keep_alive_default_minutes,keep_alive_version FROM installations WHERE singleton=1`).
		Scan(&policy.MaximumMinutes, &encoded, &policy.DefaultMinutes, &policy.Version)
	if err != nil {
		return KeepAlivePolicy{}, err
	}
	if err := json.Unmarshal([]byte(encoded), &policy.AllowedMinutes); err != nil {
		return KeepAlivePolicy{}, err
	}
	if err := policy.Validate(); err != nil {
		return KeepAlivePolicy{}, err
	}
	return policy, nil
}
