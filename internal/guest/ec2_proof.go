package guest

import (
	"context"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const (
	ec2ChallengeHeader = "X-Shiftbox-Challenge"
	ec2ChallengeTTL    = 90 * time.Second
	emptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

var (
	enrollmentRegion = regexp.MustCompile(`^(us|af|ap|ca|eu|il|me|mx|sa)-[a-z]+-[0-9]+$`)
	errEC2STS        = errors.New("EC2 enrollment identity verification is temporarily unavailable")
)

func signEC2IdentityProof(ctx context.Context, region, challenge string, credentials aws.Credentials, now time.Time) (string, error) {
	if !enrollmentRegion.MatchString(region) || !validEC2Digest(challenge) ||
		!credentials.HasKeys() || credentials.SessionToken == "" {
		return "", ErrEnrollmentDenied
	}
	endpoint := "https://sts." + region + ".amazonaws.com/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", ErrEnrollmentDenied
	}
	req.URL.RawQuery = "Action=GetCallerIdentity&Version=2011-06-15&X-Amz-Expires=60"
	req.Header.Set(ec2ChallengeHeader, challenge)
	proof, _, err := v4.NewSigner().PresignHTTP(ctx, credentials, req, emptyPayloadSHA256, "sts", region, now)
	if err != nil {
		return "", ErrEnrollmentDenied
	}
	return proof, nil
}

func verifyEC2IdentityProof(ctx context.Context, client *http.Client, region, account, instance, challenge, proof string, now time.Time) error {
	if len(proof) > 16<<10 || !enrollmentRegion.MatchString(region) || !validEC2Digest(challenge) {
		return ErrEnrollmentDenied
	}
	host := "sts." + region + ".amazonaws.com"
	u, err := url.Parse(proof)
	if err != nil || u.Scheme != "https" || u.Host != host || u.User != nil ||
		u.Path != "/" || u.RawPath != "" || u.Fragment != "" {
		return ErrEnrollmentDenied
	}
	query, err := url.ParseQuery(u.RawQuery)
	required := []string{"Action", "Version", "X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date",
		"X-Amz-Expires", "X-Amz-Security-Token", "X-Amz-Signature", "X-Amz-SignedHeaders"}
	if err != nil || len(query) != len(required) {
		return ErrEnrollmentDenied
	}
	for _, key := range required {
		if values := query[key]; len(values) != 1 || values[0] == "" {
			return ErrEnrollmentDenied
		}
	}
	if query.Get("Action") != "GetCallerIdentity" || query.Get("Version") != "2011-06-15" ||
		query.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" || query.Get("X-Amz-Expires") != "60" ||
		query.Get("X-Amz-SignedHeaders") != "host;x-shiftbox-challenge" || !validEC2Digest(query.Get("X-Amz-Signature")) {
		return ErrEnrollmentDenied
	}
	date, err := time.Parse("20060102T150405Z", query.Get("X-Amz-Date"))
	_, scope, hasScope := strings.Cut(query.Get("X-Amz-Credential"), "/")
	if err != nil || date.After(now.Add(15*time.Second)) || !now.Before(date.Add(ec2ChallengeTTL)) ||
		!hasScope || scope != date.Format("20060102")+"/"+region+"/sts/aws4_request" {
		return ErrEnrollmentDenied
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/?"+u.RawQuery, nil)
	if err != nil {
		return ErrEnrollmentDenied
	}
	req.Header.Set(ec2ChallengeHeader, challenge)
	response, err := client.Do(req)
	if err != nil {
		// The URL inside a transport error can contain an AWS session token.
		return errEC2STS
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusInternalServerError || response.StatusCode == http.StatusTooManyRequests {
		return errEC2STS
	}
	if response.StatusCode != http.StatusOK {
		return ErrEnrollmentDenied
	}
	var result struct {
		XMLName xml.Name `xml:"GetCallerIdentityResponse"`
		Caller  struct {
			Account string `xml:"Account"`
			ARN     string `xml:"Arn"`
		} `xml:"GetCallerIdentityResult"`
	}
	if err := xml.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&result); err != nil {
		return errEC2STS
	}
	expected := "arn:aws:sts::" + account + ":assumed-role/aws:ec2-instance/" + instance
	if result.Caller.Account != account || result.Caller.ARN != expected {
		return ErrEnrollmentDenied
	}
	return nil
}

func validEC2Digest(raw string) bool {
	decoded, err := hex.DecodeString(raw)
	return err == nil && len(decoded) == 32 && strings.ToLower(raw) == raw
}
