package contract_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/web"
)

// TestGuestEnrollmentHTTP checks enrollment over server-authenticated TLS,
// limits inputs, and rejects incompatible agents before issuing credentials.
func TestGuestEnrollmentHTTP(t *testing.T) {
	now := time.Now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	const token = "test-grant"
	calls := 0
	handler := web.GuestEnrollmentHandler(web.GuestCompatibility{Protocol: "1", CurrentRelease: "current", PreviousRelease: "previous"},
		func(_ context.Context, grant string, der []byte, stamp time.Time) ([]byte, error) {
			calls++
			if grant != token || !bytes.Equal(der, csr) || !stamp.Equal(now) {
				t.Error("wrong enrollment inputs")
			}
			return []byte{1, 2, 3}, nil
		}, func() time.Time { return now })
	server := httptest.NewUnstartedServer(handler)
	server.TLS = guest.EnrollmentTLS(tls.Certificate{})
	server.TLS.Certificates = nil // httptest supplies a certificate trusted by its client.
	server.StartTLS()
	defer server.Close()

	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))
	body := map[string]string{"grant": token, "csrPem": csrPEM, "guestRelease": "unsupported", "protocolVersion": "1"}
	post := func(path string, want int, code string) []byte {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := server.Client().Post(server.URL+path, "application/json", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		var problem web.Problem
		if resp.StatusCode != want || (code != "" && (json.Unmarshal(raw, &problem) != nil || problem.Code != code)) {
			t.Fatalf("POST %s = %d %s, want %d %s", path, resp.StatusCode, raw, want, code)
		}
		return raw
	}
	post("/internal/v1/enroll", 409, "guest.release_unsupported")
	body["guestRelease"] = "previous"
	body["protocolVersion"] = "2"
	post("/internal/v1/enroll", 409, "guest.release_unsupported")
	body["protocolVersion"] = "1"
	body["workspaceId"] = "forged"
	post("/internal/v1/enroll", 400, "request.invalid_body")
	delete(body, "workspaceId")
	post("/internal/v1/guest/poll", 404, "")
	if calls != 0 {
		t.Fatalf("enrollment called for invalid requests %d times", calls)
	}
	raw := post("/internal/v1/enroll", 200, "")
	var result struct {
		CertificatePEM string `json:"certificatePem"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	cert, _ := pem.Decode([]byte(result.CertificatePEM))
	if calls != 1 || cert == nil || cert.Type != "CERTIFICATE" || !bytes.Equal(cert.Bytes, []byte{1, 2, 3}) {
		t.Fatal("response did not contain the issued certificate")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/internal/v1/enroll", nil))
	if rec.Code != 403 || calls != 1 {
		t.Fatalf("plaintext request = %d, calls %d, want 403 and no issuance", rec.Code, calls)
	}
}
