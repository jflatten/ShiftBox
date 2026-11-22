package proxy

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"io"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TestTunnelFramesPreserveHTTPBytes verifies framing bounds and distinguishes
// stream metadata from the raw HTTP bytes that immediately follow it.
func TestTunnelFramesPreserveHTTPBytes(t *testing.T) {
	var buffer bytes.Buffer
	header := StreamHeader{ServiceKey: "ide", RequestID: "fixture", Protocol: "http"}
	if err := WriteTunnelFrame(&buffer, header); err != nil {
		t.Fatal(err)
	}
	const payload = "GET / HTTP/1.1\r\n\r\n"
	buffer.WriteString(payload)
	var got StreamHeader
	if err := ReadTunnelFrame(&buffer, &got); err != nil || got != header || buffer.String() != payload {
		t.Errorf("tunnel frame = %+v and %q, err %v, want %+v and untouched HTTP bytes", got, buffer.String(), err, header)
	}
	buffer.Reset()
	if err := binary.Write(&buffer, binary.BigEndian, uint32(maxTunnelFrame+1)); err != nil {
		t.Fatal(err)
	}
	if err := ReadTunnelFrame(&buffer, &got); err == nil {
		t.Error("ReadTunnelFrame(oversized frame) = nil, want rejection")
	}
}

// TestSSHTunnelService binds the SSH protocol and host key to one private
// service in an authenticated workspace generation.
func TestSSHTunnelService(t *testing.T) {
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	valid := TunnelService{Key: "ssh", Protocols: []string{"ssh"}, HostPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}
	services, err := ValidateTunnelServices([]TunnelService{valid})
	if err != nil {
		t.Fatalf("ValidateTunnelServices(SSH) = %v, want success", err)
	}
	allowed, err := NewServiceAllowlist("workspace-a", 7, services)
	if err != nil || !allowed.Allows("workspace-a", 7, "ssh", "ssh") {
		t.Fatalf("NewServiceAllowlist(SSH) = %v, %v, want scoped SSH access", allowed, err)
	}
	if allowed.Allows("workspace-b", 7, "ssh", "ssh") || allowed.Allows("workspace-a", 8, "ssh", "ssh") ||
		allowed.Allows("workspace-a", 7, "ssh", "http") {
		t.Error("SSH service accepted a different workspace, generation, or protocol")
	}
	changes := map[string]func(*TunnelService){
		"host":         func(service *TunnelService) { service.Target = "127.0.0.1:22" },
		"protocol":     func(service *TunnelService) { service.Protocols = []string{"ssh", "http"} },
		"key":          func(service *TunnelService) { service.HostPublicKey = "bad" },
		"otherService": func(service *TunnelService) { service.Key = "other" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			invalid := valid
			change(&invalid)
			if _, err := ValidateTunnelServices([]TunnelService{invalid}); err == nil {
				t.Errorf("ValidateTunnelServices(%+v) = nil, want rejection", invalid)
			}
		})
	}
	if !ValidStreamHeader(StreamHeader{ServiceKey: "ssh", Protocol: "ssh", RequestID: "fixture"}) ||
		ValidStreamHeader(StreamHeader{ServiceKey: "ide", Protocol: "ssh", RequestID: "fixture"}) {
		t.Error("SSH stream header did not enforce the fixed service")
	}
}

// TestTunnelServicesRejectRemoteTargets verifies that service declarations
// cannot introduce network destinations or unsupported protocols.
func TestTunnelServicesRejectRemoteTargets(t *testing.T) {
	if _, err := ValidateTunnelServices([]TunnelService{{Key: "preview-8080", Target: "127.0.0.1:9090", Protocols: []string{"http"}}}); err == nil {
		t.Error("preview for port 8080 accepted a different loopback port")
	}
	for _, target := range []string{"192.0.2.4:8080", "localhost:8080", "127.0.0.1:0", "[::]:8080", "/var/run/docker.sock"} {
		if _, err := ValidateTunnelServices([]TunnelService{{Key: "ide", Target: target, Protocols: []string{"http"}}}); err == nil {
			t.Errorf("ValidateTunnelServices(%q) = nil, want rejection", target)
		}
	}
	if _, err := ValidateTunnelServices([]TunnelService{{Key: "ide", Target: IDESocket, Protocols: []string{"http", "websocket"}}}); err != nil {
		t.Errorf("ValidateTunnelServices(IDE socket) = %v, want success", err)
	}
	if _, err := ValidateTunnelServices([]TunnelService{{Key: "repair", Target: "/run/shiftbox-repair/repair.sock", Protocols: []string{"http", "websocket"}}}); err != nil {
		t.Errorf("ValidateTunnelServices(repair socket) = %v, want success", err)
	}
	if _, err := ValidateTunnelServices([]TunnelService{{Key: "ide", Target: "/run/shiftbox-repair/repair.sock", Protocols: []string{"http"}}}); err == nil {
		t.Error("ValidateTunnelServices(IDE pointing to repair socket) = nil, want rejection")
	}
	if _, err := ValidateTunnelServices([]TunnelService{{Key: "preview", Target: IDESocket, Protocols: []string{"http"}}}); err == nil {
		t.Error("ValidateTunnelServices(preview pointing to IDE socket) = nil, want rejection")
	}
	if !ValidStreamHeader(StreamHeader{ServiceKey: "ide", RequestID: "fixture", Protocol: "websocket"}) ||
		ValidStreamHeader(StreamHeader{ServiceKey: "ide", RequestID: "fixture", Protocol: "tcp"}) {
		t.Error("unexpected stream protocol acceptance")
	}
	if err := WriteTunnelFrame(io.Discard, StreamHeader{ServiceKey: "ide", RequestID: "fixture", Protocol: "http"}); err != nil {
		t.Fatal(err)
	}
}
