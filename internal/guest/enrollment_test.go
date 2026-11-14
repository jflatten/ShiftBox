package guest_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/guest"
	"github.com/jflatten/ShiftBox/internal/provider"
	"github.com/jflatten/ShiftBox/internal/web"
	_ "modernc.org/sqlite"
)

// TestEnrollmentGrantReplay verifies one-use issuance, lost-response recovery
// for the same key, and denial after expiry or registration revocation.
func TestEnrollmentGrantReplay(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, database.Config{Profile: "sqlite", SQLitePath: filepath.Join(t.TempDir(), "guest.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.Migrate(ctx, db, "sqlite"); err != nil {
		t.Fatal(err)
	}
	const install = "00000000-0000-4000-8000-000000000001"
	_, err = db.Exec(`
	INSERT INTO installations(id,state,organization_name,data_profile,management_origin,ide_host_pattern,preview_host_pattern,active_release,min_guest_release,created_at,updated_at,version)
	VALUES('` + install + `','active','ShiftBox','sqlite','https://management.test','ide.test','preview.test','release','release','now','now',1);
	INSERT INTO users(id,primary_email,role,membership_state,credential_mode,created_by,created_at,updated_at,version)
	VALUES('owner','a@example.test','member','active','local','setup','now','now',1);
	INSERT INTO platform_artifacts(id,release_id,architecture,platform_version,guest_release,protocol_version,owned_resources_json,digests_json,provenance_json,manifest_json,manifest_sha256,bundle_name,imported_by,imported_at,state)
	VALUES('artifact','release','amd64','test','test','1','[]','{}','{}','{}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','fixture.utm','owner','now','available');
	INSERT INTO workspaces(id,installation_id,owner_user_id,template_id,template_revision,platform_artifact_id,repository_url,devcontainer_path,cpu_count,memory_mib,disk_mib,network_policy_snapshot_json,desired_state,observed_state,generation,ownership_marker,provider_instance_id,created_at,updated_at,version)
	VALUES('work','` + install + `','owner','template',1,'artifact','https://github.com/example/test','devcontainer.json',2,1024,40,'{}','running','stopped',1,'marker','vm','now','now',1);
	INSERT INTO operations(id,workspace_id,type,generation,requested_by,request_key,status,created_at,version)
	VALUES('op','work','provision',1,'owner','fixture-enrollment','running','now',1);
	INSERT INTO guest_registrations(workspace_id,generation,state) VALUES('work',1,'eligible');`)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, issuerKey, issuer := authorityFixture(t)
	now := time.Now().UTC()
	ca, err := guest.LoadCA(certPEM, keyPEM, now)
	if err != nil {
		t.Fatal(err)
	}
	enrollment := guest.NewEnrollment(db, ca)
	ref := provider.WorkspaceRef{InstallationID: install, WorkspaceID: "work", Generation: 1, OwnershipMarker: "marker", InstanceID: "vm"}
	inventory := provider.Inventory{Ref: ref, InstanceID: "vm", Power: provider.Stopped, ObservedAt: now}
	wrong := inventory
	wrong.Ref.OwnershipMarker = "foreign"
	if _, err := enrollment.Mint(ctx, "op", wrong, now); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("mint for mismatched ownership = %v, want denial", err)
	}
	wrong = inventory
	wrong.Power = provider.Running
	if _, err := enrollment.Mint(ctx, "op", wrong, now); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("mint for running VM = %v, want denial", err)
	}
	token, err := enrollment.Mint(ctx, "op", inventory, now)
	if err != nil {
		t.Fatal(err)
	}
	var mediaRoot, retainedImage string
	if os.Getenv("SHIFTBOX_TEST_ENROLLMENT_MEDIA") != "" {
		root := t.TempDir()
		mediaRoot = root
		failure := errors.New("provider lost delivery reply")
		_, err := enrollment.Deliver(ctx, "op", inventory, now, root, "https://internal.test/internal/v1/enroll",
			func(_ context.Context, req provider.EnrollmentRequest) (provider.DeliveryResult, error) {
				if req.Ref != ref || req.OperationID != "op" || !req.Eligible {
					t.Error("delivery did not use the verified operation and instance")
				}
				if _, err := os.Stat(req.MediaPath); err != nil {
					t.Errorf("delivery image unavailable to provider: %v", err)
				}
				return provider.DeliveryResult{}, failure
			})
		if !errors.Is(err, failure) {
			t.Fatalf("lost media delivery = %v, want provider error", err)
		}
		if files, err := os.ReadDir(root); err != nil || len(files) != 0 {
			t.Fatalf("invalidated attempt left files: %v, %v", files, err)
		}
		var issued int
		if err := db.QueryRow("SELECT COUNT(*) FROM enrollment_grants WHERE state='issued'").Scan(&issued); err != nil || issued != 0 {
			t.Fatalf("issued grants after failed delivery = %d, %v", issued, err)
		}
		if _, err := db.Exec("DELETE FROM enrollment_grants WHERE state='invalidated'"); err != nil {
			t.Fatal(err)
		}
		image, err := enrollment.Deliver(ctx, "op", inventory, now, root, "https://internal.test/internal/v1/enroll",
			func(_ context.Context, req provider.EnrollmentRequest) (provider.DeliveryResult, error) {
				data, err := os.ReadFile(req.MediaPath)
				if err != nil {
					return provider.DeliveryResult{}, err
				}
				field := []byte(`"grant":"`)
				start := bytes.Index(data, field)
				if start < 0 || start+len(field)+43 > len(data) {
					t.Fatal("delivery media omitted the grant")
				}
				token = string(data[start+len(field) : start+len(field)+43])
				return provider.DeliveryResult{Inventory: inventory, MediaID: "vm/recovery"}, nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(image); err != nil {
			t.Fatalf("successful delivery did not retain the image: %v", err)
		}
		retainedImage = image
		reopened := guest.NewEnrollment(db, ca)
		if _, err := db.Exec("UPDATE enrollment_grants SET media_attached_at=NULL WHERE state='issued'"); err != nil {
			t.Fatal(err)
		}
		resumed, err := reopened.Deliver(ctx, "op", inventory, now, root, "https://internal.test/internal/v1/enroll",
			func(_ context.Context, req provider.EnrollmentRequest) (provider.DeliveryResult, error) {
				if req.MediaPath != image {
					t.Fatal("interrupted delivery replaced the prepared image")
				}
				return provider.DeliveryResult{Inventory: inventory, MediaID: "vm/recovery"}, nil
			})
		if err != nil || resumed != image {
			t.Fatalf("interrupted delivery = %q, %v, want original prepared image", resumed, err)
		}
		resumed, err = reopened.Deliver(ctx, "op", inventory, now, root, "https://internal.test/internal/v1/enroll",
			func(context.Context, provider.EnrollmentRequest) (provider.DeliveryResult, error) {
				t.Fatal("confirmed delivery reached the provider again")
				return provider.DeliveryResult{}, nil
			})
		if err != nil || resumed != image {
			t.Fatalf("reopened delivery = %q, %v, want original attachment", resumed, err)
		}
		if err := reopened.CleanupDelivery(ctx, "op", ref, root); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(image); err != nil {
			t.Fatal("cleanup removed the unconsumed image")
		}
	}
	var digest string
	if err := db.QueryRow("SELECT id FROM enrollment_grants").Scan(&digest); err != nil || len(digest) != 64 || digest == token {
		t.Fatalf("stored grant ID length = %d, %v, want digest", len(digest), err)
	}
	csr := fixtureCSR(t)
	first := enrollOverTLS(t, ca, leafFixture(t, issuerKey, issuer, false), enrollment, token, csr, now)
	leaf, err := x509.ParseCertificate(first)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Subject.CommonName == "injected" || len(leaf.DNSNames) != 0 || len(leaf.URIs) != 1 ||
		!leaf.NotAfter.Equal(issuer.NotAfter) || leaf.CheckSignatureFrom(issuer) != nil ||
		len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Errorf("issued certificate did not enforce controller identity and issuer validity: %+v", leaf)
	}
	replay, err := enrollment.Enroll(ctx, token, csr, now.Add(time.Minute))
	if err != nil || !bytes.Equal(replay, first) {
		t.Errorf("same-key retry = %v, want exact issued certificate", err)
	}
	if retainedImage != "" {
		reopened := guest.NewEnrollment(db, ca)
		for range 2 {
			if err := reopened.CleanupDelivery(ctx, "op", ref, mediaRoot); err != nil {
				t.Fatalf("cleanup confirmed enrollment: %v", err)
			}
		}
		if _, err := os.Stat(retainedImage); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("media after cleanup = %v, want absent", err)
		}
	}
	if _, err := enrollment.Enroll(ctx, token, fixtureCSR(t), now); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("alternate key = %v, want denial", err)
	}
	if _, err := enrollment.Enroll(ctx, token, csr, now.Add(15*time.Minute)); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("expired grant = %v, want denial", err)
	}
	if _, err := db.Exec("UPDATE guest_registrations SET state='revoked' WHERE workspace_id='work'"); err != nil {
		t.Fatal(err)
	}
	if _, err := enrollment.Enroll(ctx, token, csr, now); !errors.Is(err, guest.ErrEnrollmentDenied) {
		t.Errorf("revoked registration replay = %v, want denial", err)
	}
}

func enrollOverTLS(t *testing.T, ca *guest.Authority, serverLeaf tls.Certificate, enrollment *guest.Enrollment, token string, csr []byte, now time.Time) []byte {
	t.Helper()
	handler := web.GuestEnrollmentHandler(web.GuestCompatibility{Protocol: "1", CurrentRelease: "test"},
		enrollment.Enroll, func() time.Time { return now })
	server := httptest.NewUnstartedServer(handler)
	server.TLS = guest.EnrollmentTLS(serverLeaf)
	server.StartTLS()
	t.Cleanup(server.Close)
	client := server.Client()
	transport := client.Transport.(*http.Transport)
	noClientCert := guest.ClientTLS(tls.Certificate{}, ca, "controller.test")
	noClientCert.Certificates = nil
	transport.TLSClientConfig = noClientCert
	serverCertRequest := map[string]string{"protocolVersion": "1", "guestRelease": "test", "grant": token,
		"csrPem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))}
	body, err := json.Marshal(serverCertRequest)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post(server.URL+"/internal/v1/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("real enrollment returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		CertificatePEM string `json:"certificatePem"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	cert, _ := pem.Decode([]byte(result.CertificatePEM))
	if cert == nil || cert.Type != "CERTIFICATE" {
		t.Fatal("enrollment response omitted the certificate")
	}
	return cert.Bytes
}

func fixtureCSR(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "injected"}, DNSNames: []string{"injected.test"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	return csr
}
