package config

import (
	"errors"
	"strings"
)

// ValidateServiceHostPatterns accepts the installation's two fixed templates.
// Workspace UUIDs and preview service keys occupy one DNS label, so ordinary
// wildcard certificates can cover each configured suffix.
func ValidateServiceHostPatterns(ide, preview string) error {
	for _, pattern := range []struct {
		value, prefix string
		labelLength   int
	}{
		{ide, "{workspace}.", 36},
		{preview, "{service}--{workspace}.", 63},
	} {
		suffix, ok := strings.CutPrefix(pattern.value, pattern.prefix)
		// Reserve the generated label and its separating dot.
		if !ok || len(suffix) == 0 || len(suffix) > 253-pattern.labelLength-1 {
			return errors.New("invalid service hostname pattern")
		}
		for _, label := range strings.Split(suffix, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return errors.New("invalid service hostname suffix")
			}
			for _, ch := range label {
				if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
					return errors.New("service hostname suffix must use lowercase DNS labels")
				}
			}
		}
	}
	return nil
}
