package workspace

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
)

// ParsePreviewPorts validates and sorts a manually configured set of TCP ports.
func ParsePreviewPorts(raw string) ([]int, error) {
	var ports []int
	// Decode individual numbers so a JSON null cannot become a valid value.
	if err := json.Unmarshal([]byte(raw), &ports); err != nil || ports == nil || len(ports) > 32 {
		return nil, errors.New("provide up to 32 distinct TCP ports")
	}
	slices.Sort(ports)
	for i, port := range ports {
		if port < 1 || port > 65535 || (i > 0 && port == ports[i-1]) {
			return nil, errors.New("provide up to 32 distinct TCP ports")
		}
	}
	return ports, nil
}

// PreviewPort accepts only canonical service names for manually configured ports.
func PreviewPort(service string) (int, bool) {
	text, found := strings.CutPrefix(service, "preview-")
	port, err := strconv.Atoi(text)
	return port, found && err == nil && port >= 1 && port <= 65535 && strconv.Itoa(port) == text
}
