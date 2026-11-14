package guest_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
)

// TestAgentPollUsesClientIdentity checks that the agent proves its identity
// to the internal listener and sends the expected protocol and release.
func TestAgentPollUsesClientIdentity(t *testing.T) {
	certPEM, keyPEM, key, cert := authorityFixture(t)
	ca, err := guest.LoadCA(certPEM, keyPEM, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil ||
			request["protocolVersion"] != "1" || request["guestRelease"] != "test" ||
			r.Method != http.MethodPost || r.URL.Path != "/internal/v1/guest/poll" || len(r.TLS.VerifiedChains) == 0 {
			t.Error("unexpected authenticated poll request")
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write([]byte(`{"protocolVersion":"1","workspaceGeneration":1,"desiredState":"running","serverTime":"2026-11-14T00:00:00Z","operation":null,"certificate":{"renewAfter":"2026-11-14T04:00:00Z"},"nextPollAfterSeconds":30}`))
	}))
	server.TLS = guest.ServerTLS(leafFixture(t, key, cert, false), ca)
	server.StartTLS()
	defer server.Close()
	clientLeaf := leafFixture(t, key, cert, true)
	config := guest.ClientTLS(clientLeaf, ca, "controller.test")
	identity := guest.AgentIdentity{Certificate: clientLeaf, Roots: config.RootCAs}
	if err := identity.Poll(t.Context(), server.URL+"/internal/v1/guest/poll", "controller.test", "1", "test"); err != nil {
		t.Fatalf("authenticated poll: %v", err)
	}
}
