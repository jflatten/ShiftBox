package guest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const agentPayloadLimit = 16 << 10

// AgentIdentity is the guest's persisted client identity and controller trust.
type AgentIdentity struct {
	Certificate  tls.Certificate
	Roots        *x509.CertPool
	PollEndpoint string
}

type agentIdentityFile struct {
	CertificatePEM string `json:"certificatePem"`
	KeyPEM         []byte `json:"keyPem"`
	CAPEM          string `json:"caPem"`
	PollEndpoint   string `json:"pollEndpoint"`
}

// EnrollAgent exchanges a recovery seed for a certificate. The guest retains
// its pending private key so a retry can recover a lost issuance response.
func EnrollAgent(ctx context.Context, directory string, input io.Reader, protocol, release string, now time.Time) (AgentIdentity, error) {
	var seed EnrollmentSeed
	if err := decodeAgentJSON(input, &seed); err != nil || !seed.valid() {
		return AgentIdentity{}, ErrInvalidEnrollmentSeed
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return AgentIdentity{}, err
	}
	keyPath := filepath.Join(directory, "pending-key.json")
	digest := sha256.Sum256([]byte(seed.Grant))
	keyPEM, key, err := agentEnrollmentKey(keyPath, hex.EncodeToString(digest[:]))
	if err != nil {
		return AgentIdentity{}, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return AgentIdentity{}, err
	}
	body, err := json.Marshal(map[string]string{
		"grant": seed.Grant, "protocolVersion": protocol, "guestRelease": release,
		"csrPem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})),
	})
	if err != nil {
		return AgentIdentity{}, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(seed.CAPEM)) {
		return AgentIdentity{}, ErrInvalidEnrollmentSeed
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := http.Client{
		Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, seed.Endpoint, bytes.NewReader(body))
	if err != nil {
		return AgentIdentity{}, ErrInvalidEnrollmentSeed
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return AgentIdentity{}, ctx.Err()
		}
		return AgentIdentity{}, errors.New("guest enrollment HTTPS exchange failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return AgentIdentity{}, ErrEnrollmentDenied
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
		CertificatePEM  string `json:"certificatePem"`
	}
	if err := decodeAgentJSON(response.Body, &result); err != nil || result.ProtocolVersion != protocol {
		return AgentIdentity{}, errors.New("invalid guest enrollment response")
	}

	record := agentIdentityFile{CertificatePEM: result.CertificatePEM, KeyPEM: keyPEM, CAPEM: seed.CAPEM, PollEndpoint: seed.PollEndpoint}
	identity, err := record.identity(now)
	if err != nil {
		return AgentIdentity{}, err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return AgentIdentity{}, err
	}
	if err := writePrivateState(filepath.Join(directory, "identity.json"), data); err != nil {
		return AgentIdentity{}, err
	}
	if err := os.Remove(keyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return AgentIdentity{}, err
	}
	return identity, nil
}

// LoadAgentIdentity reopens a valid enrolled identity without recovery media.
func LoadAgentIdentity(directory string, now time.Time) (AgentIdentity, error) {
	file, err := os.Open(filepath.Join(directory, "identity.json"))
	if err != nil {
		return AgentIdentity{}, err
	}
	defer file.Close()
	var record agentIdentityFile
	if err := decodeAgentJSON(file, &record); err != nil {
		return AgentIdentity{}, err
	}
	return record.identity(now)
}

func (r agentIdentityFile) identity(now time.Time) (AgentIdentity, error) {
	if !validPollEndpoint(r.PollEndpoint) {
		return AgentIdentity{}, errors.New("invalid stored guest poll endpoint")
	}
	pair, err := tls.X509KeyPair([]byte(r.CertificatePEM), r.KeyPEM)
	if err != nil || pair.Leaf == nil {
		return AgentIdentity{}, errors.New("invalid guest identity pair")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(r.CAPEM)) {
		return AgentIdentity{}, ErrInvalidEnrollmentSeed
	}
	if _, err := pair.Leaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return AgentIdentity{}, errors.New("guest identity validation failed")
	}
	return AgentIdentity{Certificate: pair, Roots: roots, PollEndpoint: r.PollEndpoint}, nil
}

func agentEnrollmentKey(path, grantDigest string) ([]byte, *ecdsa.PrivateKey, error) {
	var pending struct {
		GrantDigest string `json:"grantDigest"`
		KeyPEM      []byte `json:"keyPem"`
	}
	file, err := os.Open(path)
	if err == nil {
		err = errors.Join(decodeAgentJSON(file, &pending), file.Close())
		if err != nil {
			return nil, nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	if pending.GrantDigest != grantDigest {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return nil, nil, err
		}
		data := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
		pending.GrantDigest, pending.KeyPEM = grantDigest, data
		wire, err := json.Marshal(pending)
		if err != nil {
			return nil, nil, err
		}
		if err := writePrivateState(path, wire); err != nil {
			return nil, nil, err
		}
		return data, key, nil
	}
	block, _ := pem.Decode(pending.KeyPEM)
	if block == nil || block.Type != "EC PRIVATE KEY" {
		return nil, nil, errors.New("invalid pending guest key")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil || key.Curve != elliptic.P256() {
		return nil, nil, errors.New("invalid pending guest key")
	}
	return pending.KeyPEM, key, nil
}

func decodeAgentJSON(input io.Reader, value any) error {
	raw, err := io.ReadAll(io.LimitReader(input, agentPayloadLimit+1))
	if err != nil || len(raw) > agentPayloadLimit {
		return errors.New("invalid guest payload")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid guest payload")
	}
	return nil
}

func writePrivateState(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".guest-state-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
