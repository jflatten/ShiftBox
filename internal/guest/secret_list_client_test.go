package guest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestLocalSecretList traverses the Unix credential socket and authenticated
// controller transport, rejects value-bearing replies, and never caches keys.
func TestLocalSecretList(t *testing.T) {
	status := http.StatusOK
	body := `{"keys":[{"name":"ALPHA","scope":"project"},{"name":"TOKEN","scope":"user"}]}`
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/internal/v1/guest/secrets" {
			t.Errorf("list request=%s %s,want GET secrets", r.Method, r.URL.RequestURI())
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.Close)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	path := filepath.Join(t.TempDir(), "secrets.sock")
	stop, err := listenSecretSocket(ctx, path, upstream.Client(), upstream.URL+pollPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	got, err := ListLocalSecrets(ctx, path)
	want := []SecretKey{{Name: "ALPHA", Scope: "project"}, {Name: "TOKEN", Scope: "user"}}
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ListLocalSecrets mismatch (-want +got):\n%s", diff)
	}
	for _, invalid := range []string{
		`{"keys":[{"name":"TOKEN","scope":"user","value":"must-not-leak"}]}`,
		`{"keys":[{"name":"TOKEN","scope":"workspace"}]}`,
		`{"keys":[{"name":"bad name","scope":"user"}]}`,
		`{"keys":null}`,
	} {
		body = invalid
		if got, err := ListLocalSecrets(ctx, path); err == nil || len(got) != 0 {
			t.Errorf("ListLocalSecrets invalid body=%v,%v,want failure/no keys", got, err)
		}
	}
	body = `{"keys":[]}`
	if got, err := ListLocalSecrets(ctx, path); err != nil || len(got) != 0 {
		t.Errorf("ListLocalSecrets empty=%v,%v,want empty success", got, err)
	}
	status = http.StatusForbidden
	if got, err := ListLocalSecrets(ctx, path); !errors.Is(err, ErrGuestUnauthorized) || len(got) != 0 {
		t.Errorf("ListLocalSecrets denied=%v,%v,want denial/no keys", got, err)
	}
	upstream.Close()
	if got, err := ListLocalSecrets(ctx, path); err == nil || len(got) != 0 {
		t.Errorf("ListLocalSecrets after outage=%v,%v,want unavailable/no keys", got, err)
	}
}
