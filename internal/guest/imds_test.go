package guest

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestEC2EnrollmentSeedUsesIMDSv2 checks that the enrollment payload is read
// with the instance's scoped metadata token and kept off disk.
func TestEC2EnrollmentSeedUsesIMDSv2(t *testing.T) {
	var paths []string
	client := &http.Client{Transport: metadataTransport(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		body := "metadata-token"
		if r.Method == http.MethodPut && r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") != "60" {
			t.Error("metadata token request did not set a short TTL")
		}
		if r.Method == http.MethodGet {
			if r.Header.Get("X-aws-ec2-metadata-token") != "metadata-token" {
				t.Error("metadata read used no IMDSv2 token")
			}
			body = `{"grant":"synthetic-fixture"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	value, err := readEC2EnrollmentSeed(t.Context(), client)
	if err != nil || string(value) != `{"grant":"synthetic-fixture"}` ||
		strings.Join(paths, ",") != "PUT /latest/api/token,GET /latest/user-data" {
		t.Fatalf("IMDS seed route = %v, %v", paths, err)
	}
}

type metadataTransport func(*http.Request) (*http.Response, error)

func (f metadataTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
