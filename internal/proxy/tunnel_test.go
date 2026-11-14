package proxy

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
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

// TestTunnelServicesRejectRemoteTargets verifies that service declarations
// cannot introduce network destinations or unsupported protocols.
func TestTunnelServicesRejectRemoteTargets(t *testing.T) {
	for _, target := range []string{"192.0.2.4:8080", "localhost:8080", "127.0.0.1:0", "[::]:8080"} {
		if _, err := ValidateTunnelServices([]TunnelService{{Key: "ide", Target: target, Protocols: []string{"http"}}}); err == nil {
			t.Errorf("ValidateTunnelServices(%q) = nil, want rejection", target)
		}
	}
	if !ValidStreamHeader(StreamHeader{ServiceKey: "ide", RequestID: "fixture", Protocol: "websocket"}) ||
		ValidStreamHeader(StreamHeader{ServiceKey: "ide", RequestID: "fixture", Protocol: "tcp"}) {
		t.Error("unexpected stream protocol acceptance")
	}
	if err := WriteTunnelFrame(io.Discard, StreamHeader{ServiceKey: "ide", RequestID: "fixture", Protocol: "http"}); err != nil {
		t.Fatal(err)
	}
}
