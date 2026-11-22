package guest

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// TestEC2IdentityProof verifies the forwarded action, mandatory signed challenge, freshness and exact AWS identity.
func TestEC2IdentityProof(t *testing.T) {
	now := time.Date(2026, 11, 17, 16, 30, 0, 0, time.UTC)
	const challenge = "fd46fd30d057276a8cd1ec8f77ffbed0391d9531dcecd39a0d5b72002b14c067"
	const account = "524941137496"
	const instance = "i-0a03ffcd5e48bad74"
	const region = "us-east-2"
	proof, err := signEC2IdentityProof(t.Context(), region, challenge, aws.Credentials{
		AccessKeyID: "ASIAEXAMPLE000000000", SecretAccessKey: "synthetic secret", SessionToken: "synthetic session",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	const validARN = "arn:aws:sts::" + account + ":assumed-role/aws:ec2-instance/" + instance
	cases := []struct {
		name, host, modification, arn string
		age                           time.Duration
		wantCalls                     int
		want                          bool
	}{
		{name: "verified", arn: validARN, wantCalls: 1, want: true},
		{name: "other instance", arn: validARN + "0", wantCalls: 1},
		{name: "unrelated role with same session", arn: "arn:aws:sts::" + account + ":assumed-role/other/" + instance, wantCalls: 1},
		{name: "untrusted host", host: "localhost"},
		{name: "unsigned challenge", modification: "unsigned"},
		{name: "unexpected action", modification: "action"},
		{name: "duplicate query key", modification: "duplicate"},
		{name: "stale proof", age: 2 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(proof)
			if err != nil {
				t.Fatal(err)
			}
			if tc.host != "" {
				u.Host = tc.host
			}
			query := u.Query()
			switch tc.modification {
			case "unsigned":
				query.Set("X-Amz-SignedHeaders", "host")
			case "action":
				query.Set("Action", "AssumeRole")
			case "duplicate":
				query.Add("Action", "GetCallerIdentity")
			}
			u.RawQuery = query.Encode()
			var calls int
			client := &http.Client{Transport: proofRoundTripper(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Host != "sts.us-east-2.amazonaws.com" || req.URL.Query().Get("Action") != "GetCallerIdentity" ||
					req.Header.Get(ec2ChallengeHeader) != challenge {
					t.Error("forwarded request did not target STS with the exact signed challenge")
				}
				response := "<GetCallerIdentityResponse><GetCallerIdentityResult><Account>" + account + "</Account><Arn>" + tc.arn + "</Arn></GetCallerIdentityResult></GetCallerIdentityResponse>"
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response))}, nil
			})}
			err = verifyEC2IdentityProof(t.Context(), client, region, account, instance, challenge, u.String(), now.Add(tc.age))
			if (err == nil) != tc.want || calls != tc.wantCalls {
				t.Errorf("verifyEC2IdentityProof(%s) = %v with %d STS calls, want success %t with %d calls", tc.name, err, calls, tc.want, tc.wantCalls)
			}
		})
	}
}

type proofRoundTripper func(*http.Request) (*http.Response, error)

func (p proofRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return p(req)
}
