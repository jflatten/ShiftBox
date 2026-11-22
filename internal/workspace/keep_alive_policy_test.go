package workspace

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestKeepAlivePolicyValidation rejects settings that could offer an invalid
// deadline, bypass an administrator limit, or leave no selectable default.
func TestKeepAlivePolicyValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy KeepAlivePolicy
		valid  bool
	}{
		{name: "unlimited", policy: KeepAlivePolicy{MaximumMinutes: 0, AllowedMinutes: []int64{30, 60, 0}, DefaultMinutes: 30}, valid: true},
		{name: "finite", policy: KeepAlivePolicy{MaximumMinutes: 120, AllowedMinutes: []int64{30, 120}, DefaultMinutes: 120}, valid: true},
		{name: "unlimited default", policy: KeepAlivePolicy{MaximumMinutes: 0, AllowedMinutes: []int64{30, 0}, DefaultMinutes: 0}, valid: true},
		{name: "negative maximum", policy: KeepAlivePolicy{MaximumMinutes: -1, AllowedMinutes: []int64{30}, DefaultMinutes: 30}},
		{name: "overflow", policy: KeepAlivePolicy{MaximumMinutes: 153722868, AllowedMinutes: []int64{30}, DefaultMinutes: 30}},
		{name: "empty options", policy: KeepAlivePolicy{MaximumMinutes: 120, DefaultMinutes: 30}},
		{name: "over maximum", policy: KeepAlivePolicy{MaximumMinutes: 60, AllowedMinutes: []int64{30, 120}, DefaultMinutes: 30}},
		{name: "unlimited bypass", policy: KeepAlivePolicy{MaximumMinutes: 60, AllowedMinutes: []int64{30, 0}, DefaultMinutes: 30}},
		{name: "negative option", policy: KeepAlivePolicy{AllowedMinutes: []int64{-30, 30}, DefaultMinutes: 30}},
		{name: "duplicate", policy: KeepAlivePolicy{AllowedMinutes: []int64{30, 30}, DefaultMinutes: 30}},
		{name: "missing default", policy: KeepAlivePolicy{AllowedMinutes: []int64{30}, DefaultMinutes: 60}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.policy.Validate(); (err == nil) != tc.valid {
				t.Errorf("KeepAlivePolicy.Validate(%+v) = %v, want valid=%t", tc.policy, err, tc.valid)
			}
		})
	}
}

// TestKeepAlivePolicyEffectiveMinutes ensures inherited and removed selections
// follow the current default while explicit unlimited remains a valid choice.
func TestKeepAlivePolicyEffectiveMinutes(t *testing.T) {
	policy := KeepAlivePolicy{AllowedMinutes: []int64{30, 60, 0}, DefaultMinutes: 30}
	for _, tc := range []struct {
		override *int64
		want     int64
	}{
		{override: nil, want: 30}, {override: new(int64(60)), want: 60},
		{override: new(int64(120)), want: 30}, {override: new(int64(0)), want: 0},
	} {
		if got := policy.EffectiveMinutes(tc.override); got != tc.want {
			t.Errorf("EffectiveMinutes(%v) = %d, want %d", tc.override, got, tc.want)
		}
	}
}

// TestReadKeepAlivePolicy verifies newly initialized installations expose the
// selected initial settings in both supported database profiles.
func TestReadKeepAlivePolicy(t *testing.T) {
	for _, profile := range []string{"sqlite", "postgres"} {
		t.Run(profile, func(t *testing.T) {
			db := openWorkspaceDatabase(t, profile)
			got, err := ReadKeepAlivePolicy(t.Context(), db)
			want := KeepAlivePolicy{MaximumMinutes: 0, AllowedMinutes: []int64{30, 60, 120, 240, 480, 720, 0}, DefaultMinutes: 30, Version: 1}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("ReadKeepAlivePolicy() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
