package config_test

import (
	"strings"
	"testing"

	"github.com/jflatten/ShiftBox/internal/config"
)

// TestServiceHostPatterns rejects ambiguous templates and invalid DNS suffixes before activation.
func TestServiceHostPatterns(t *testing.T) {
	for _, tc := range []struct {
		ide, preview string
		wantError    bool
	}{
		{"{workspace}.ide.example.com", "{service}--{workspace}.preview.example.com", false},
		{"{workspace}.ide.example.com", "{service}--{workspace}.ide.example.com", false},
		{"{workspace}.ide.example.com", "{workspace}.{service}.preview.example.com", true},
		{"https://{workspace}.ide.example.com", "{service}--{workspace}.preview.example.com", true},
		{"{workspace}.IDE.example.com", "{service}--{workspace}.preview.example.com", true},
		{"{workspace}.*.example.com", "{service}--{workspace}.preview.example.com", true},
		{"{workspace}.-ide.example.com", "{service}--{workspace}.preview.example.com", true},
		{"{workspace}.ide.example.com.", "{service}--{workspace}.preview.example.com", true},
		{"{workspace}.ide:443", "{service}--{workspace}.preview.example.com", true},
		{"{workspace}." + strings.Repeat("a", 64) + ".com", "{service}--{workspace}.preview.example.com", true},
	} {
		if err := config.ValidateServiceHostPatterns(tc.ide, tc.preview); (err != nil) != tc.wantError {
			t.Errorf("ValidateServiceHostPatterns(%q, %q) = %v, want error %t", tc.ide, tc.preview, err, tc.wantError)
		}
	}
}
