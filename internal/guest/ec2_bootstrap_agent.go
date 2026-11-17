package guest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

const ec2IdentityCredentials = "/latest/meta-data/identity-credentials/ec2/security-credentials/ec2-instance"

func enrollEC2Launch(ctx context.Context, metadata *http.Client, directory string, seed ec2LaunchData, protocol, release string) (AgentIdentity, error) {
	if !seed.valid() {
		return AgentIdentity{}, ErrInvalidEnrollmentSeed
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return AgentIdentity{}, err
	}
	keyPath := filepath.Join(directory, "pending-key.json")
	keyPEM, key, err := agentEnrollmentKey(keyPath, seed.Reference)
	if err != nil {
		return AgentIdentity{}, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return AgentIdentity{}, err
	}
	client, err := enrollmentClient(seed.CAPEM)
	if err != nil {
		return AgentIdentity{}, err
	}
	defer client.CloseIdleConnections()
	proof := map[string]string{"launchReference": seed.Reference, "protocolVersion": protocol, "guestRelease": release,
		"csrPem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))}
	var challenge EC2EnrollmentChallenge
	if err := postEnrollmentJSON(ctx, client, seed.Endpoint+"/ec2/challenge", proof, &challenge); err != nil {
		return AgentIdentity{}, err
	}
	if !validEC2Digest(challenge.Nonce) || !validEC2Digest(challenge.ProofContext) || !enrollmentRegion.MatchString(challenge.Region) {
		return AgentIdentity{}, ErrEnrollmentDenied
	}
	credentials, err := readEC2IdentityCredentials(ctx, metadata)
	if err != nil {
		return AgentIdentity{}, err
	}
	signed, err := signEC2IdentityProof(ctx, challenge.Region, challenge.ProofContext, credentials, time.Now())
	if err != nil {
		return AgentIdentity{}, err
	}
	digest := sha256.Sum256([]byte(challenge.ProofContext))
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		return AgentIdentity{}, err
	}
	payload := struct {
		Reference string `json:"launchReference"`
		Protocol  string `json:"protocolVersion"`
		Release   string `json:"guestRelease"`
		CSR       string `json:"csrPem"`
		Nonce     string `json:"nonce"`
		Signature []byte `json:"signature"`
		Proof     string `json:"stsProof"`
	}{seed.Reference, protocol, release, proof["csrPem"], challenge.Nonce, sig, signed}
	var grant EC2EnrollmentGrant
	if err := postEnrollmentJSON(ctx, client, seed.Endpoint+"/ec2/grant", payload, &grant); err != nil {
		return AgentIdentity{}, err
	}
	certificate := grant.CertificatePEM
	if grant.Grant != "" && certificate == "" {
		certificate, err = requestAgentCertificate(ctx, client, seed.Endpoint, grant.Grant, protocol, release, csr)
		if err != nil {
			return AgentIdentity{}, err
		}
	}
	if certificate == "" {
		return AgentIdentity{}, ErrEnrollmentDenied
	}
	record := agentIdentityFile{CertificatePEM: certificate, KeyPEM: keyPEM, CAPEM: seed.CAPEM, PollEndpoint: seed.PollEndpoint}
	return persistAgentEnrollment(directory, keyPath, record, time.Now())
}

func readEC2IdentityCredentials(ctx context.Context, client *http.Client) (aws.Credentials, error) {
	data, err := readEC2Metadata(ctx, client, ec2IdentityCredentials, 16<<10)
	if err != nil {
		return aws.Credentials{}, err
	}
	defer clear(data)
	var identity struct {
		Code            string
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string
		Token           string
		Expiration      time.Time
	}
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&identity); err != nil || identity.Code != "Success" ||
		!time.Now().Before(identity.Expiration) || identity.AccessKeyID == "" || identity.SecretAccessKey == "" || identity.Token == "" {
		return aws.Credentials{}, ErrEnrollmentDenied
	}
	return aws.Credentials{AccessKeyID: identity.AccessKeyID, SecretAccessKey: identity.SecretAccessKey,
		SessionToken: identity.Token, CanExpire: true, Expires: identity.Expiration}, nil
}

func readEC2LaunchData(data []byte) (ec2LaunchData, bool, error) {
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return ec2LaunchData{}, false, ErrInvalidEnrollmentSeed
	}
	if header.Version == 0 {
		return ec2LaunchData{}, false, nil
	}
	var value ec2LaunchData
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&value); err != nil || dec.Decode(new(any)) != io.EOF || !value.valid() {
		return ec2LaunchData{}, true, ErrInvalidEnrollmentSeed
	}
	return value, true, nil
}
