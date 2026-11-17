package guest

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// EC2Bootstrap handles initial enrollment for a running, verified EC2 launch.
type EC2Bootstrap struct {
	enrollment      *Enrollment
	region, account string
	now             func() time.Time
	inspect         func(context.Context, provider.WorkspaceRef) (provider.Inventory, error)
	client          *http.Client
}

// NewEC2Bootstrap binds the verifier to one deployment and its private guest listener.
func NewEC2Bootstrap(enrollment *Enrollment, region, account string, now func() time.Time,
	inspect func(context.Context, provider.WorkspaceRef) (provider.Inventory, error),
) *EC2Bootstrap {
	return &EC2Bootstrap{enrollment: enrollment, region: region, account: account, now: now, inspect: inspect,
		client: &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// EC2LaunchData produces the public user data for one authorized launch.
func (e *Enrollment) EC2LaunchData(reference, endpoint, pollEndpoint string) (string, error) {
	if e.authority == nil {
		return "", ErrInvalidEnrollmentSeed
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: e.authority.Certificate.Raw})
	info := ec2LaunchData{Version: 1, Reference: reference, Endpoint: endpoint, PollEndpoint: pollEndpoint, CAPEM: string(ca)}
	if !info.valid() {
		return "", ErrInvalidEnrollmentSeed
	}
	data, err := json.Marshal(info)
	return string(data), err
}

type ec2LaunchData struct {
	Version      int    `json:"version"`
	Reference    string `json:"launchReference"`
	Endpoint     string `json:"endpoint"`
	PollEndpoint string `json:"pollEndpoint"`
	CAPEM        string `json:"caPem"`
}

func (s ec2LaunchData) valid() bool {
	return s.Version == 1 && validEC2Digest(s.Reference) && validEnrollmentEndpoints(s.Endpoint, s.PollEndpoint, s.CAPEM)
}

// EC2EnrollmentChallenge binds the guest key to the controller's validated launch.
type EC2EnrollmentChallenge struct {
	Nonce        string `json:"nonce"`
	ProofContext string `json:"proofContext"`
	Region       string `json:"region"`
}

// EC2EnrollmentGrant returns a key-bound grant, or the already-issued public certificate
// for the same key after a lost enrollment response.
type EC2EnrollmentGrant struct {
	Grant          string `json:"grant,omitempty"`
	CertificatePEM string `json:"certificatePem,omitempty"`
}

type ec2BootstrapLaunch struct {
	ref                 provider.WorkspaceRef
	operation, artifact string
	boundKey, state     string
}

const ec2Timestamp = "2006-01-02T15:04:05.000000000Z"

func ec2Stamp(t time.Time) string { return t.UTC().Format(ec2Timestamp) }

func ec2Random() ([32]byte, error) {
	var value [32]byte
	_, err := rand.Read(value[:])
	return value, err
}

// Challenge records a fresh nonce only after the EC2 launch has reached enrollment.
func (b *EC2Bootstrap) Challenge(ctx context.Context, reference string, csr []byte) (EC2EnrollmentChallenge, error) {
	_, fingerprint, err := guestKeyProof(csr)
	if err != nil || !validEC2Digest(reference) {
		return EC2EnrollmentChallenge{}, ErrEnrollmentDenied
	}
	now := b.now()
	random, err := ec2Random()
	if err != nil {
		return EC2EnrollmentChallenge{}, err
	}
	nonce := hex.EncodeToString(random[:])
	var launch ec2BootstrapLaunch
	err = database.Transaction(ctx, b.enrollment.db, func(tx *sql.Tx) error {
		var err error
		launch, err = b.target(ctx, tx, reference, fingerprint)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM ec2_launch_challenges WHERE operation_id=$1 AND expires_at<=$2`, launch.operation, ec2Stamp(now)); err != nil {
			return err
		}
		// A duplicate challenge request for the same key returns the still-live nonce.
		return tx.QueryRowContext(ctx, `INSERT INTO ec2_launch_challenges(nonce,operation_id,guest_key_proof,issued_at,expires_at)
			VALUES($1,$2,$3,$4,$5)
			ON CONFLICT(operation_id,guest_key_proof) DO UPDATE SET guest_key_proof=excluded.guest_key_proof
			RETURNING nonce`, nonce, launch.operation, fingerprint, ec2Stamp(now), ec2Stamp(now.Add(ec2ChallengeTTL))).Scan(&nonce)
	})
	if err != nil {
		return EC2EnrollmentChallenge{}, err
	}
	return EC2EnrollmentChallenge{Nonce: nonce, ProofContext: ec2ProofContext(reference, nonce, fingerprint, launch), Region: b.region}, nil
}

// Grant accepts the guest key proof and an STS-verified signature from the exact instance.
func (b *EC2Bootstrap) Grant(ctx context.Context, reference, nonce string, csr, signature []byte, signedRequest string) (EC2EnrollmentGrant, error) {
	key, fingerprint, err := guestKeyProof(csr)
	if err != nil || !validEC2Digest(reference) || !validEC2Digest(nonce) || len(signature) > 80 {
		return EC2EnrollmentGrant{}, ErrEnrollmentDenied
	}
	launch, err := b.target(ctx, b.enrollment.db, reference, fingerprint)
	if err != nil {
		return EC2EnrollmentGrant{}, err
	}
	var issued, expiry string
	err = b.enrollment.db.QueryRowContext(ctx, `SELECT issued_at,expires_at FROM ec2_launch_challenges
		WHERE nonce=$1 AND operation_id=$2 AND guest_key_proof=$3`, nonce, launch.operation, fingerprint).Scan(&issued, &expiry)
	if err != nil {
		return EC2EnrollmentGrant{}, denyMissing(err)
	}
	now := b.now()
	if err := checkEC2Challenge(issued, expiry, now); err != nil {
		return EC2EnrollmentGrant{}, err
	}
	challenge := ec2ProofContext(reference, nonce, fingerprint, launch)
	digest := sha256.Sum256([]byte(challenge))
	if !ecdsa.VerifyASN1(key, digest[:], signature) {
		return EC2EnrollmentGrant{}, ErrEnrollmentDenied
	}
	if err := verifyEC2IdentityProof(ctx, b.client, b.region, b.account, launch.ref.InstanceID, challenge, signedRequest, now); err != nil {
		return EC2EnrollmentGrant{}, err
	}
	inventory, err := b.inspect(ctx, launch.ref)
	if err != nil {
		if errors.Is(err, provider.ErrUnavailable) || errors.Is(err, provider.ErrOutcomeUnknown) {
			return EC2EnrollmentGrant{}, errEC2STS
		}
		return EC2EnrollmentGrant{}, ErrEnrollmentDenied
	}
	if !inventory.EC2Enrollment || inventory.Ref != launch.ref || inventory.InstanceID != launch.ref.InstanceID ||
		inventory.ArtifactID != launch.artifact || inventory.Power != provider.Running {
		return EC2EnrollmentGrant{}, ErrEnrollmentDenied
	}
	return b.issue(ctx, reference, nonce, fingerprint, launch)
}

func (b *EC2Bootstrap) issue(ctx context.Context, reference, nonce, fingerprint string, verified ec2BootstrapLaunch) (EC2EnrollmentGrant, error) {
	now := b.now()
	random, err := ec2Random()
	if err != nil {
		return EC2EnrollmentGrant{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	var response EC2EnrollmentGrant
	err = database.Transaction(ctx, b.enrollment.db, func(tx *sql.Tx) error {
		// Serialize with lifecycle transitions and certificate issuance.
		result, err := tx.ExecContext(ctx, `UPDATE operations SET version=version WHERE id=$1
			AND status IN ('running','waiting') AND current_step='enroll'`, verified.operation)
		if err := mediaWrite(result, err); err != nil {
			return ErrEnrollmentDenied
		}
		if err := lockRegistration(ctx, tx, verified.ref.WorkspaceID, verified.ref.Generation); err != nil {
			return err
		}
		launch, err := b.target(ctx, tx, reference, fingerprint)
		if err != nil {
			return err
		}
		if launch.ref != verified.ref || launch.operation != verified.operation || launch.artifact != verified.artifact {
			return ErrEnrollmentDenied
		}
		var issued, expiry string
		err = tx.QueryRowContext(ctx, `DELETE FROM ec2_launch_challenges WHERE nonce=$1 AND operation_id=$2 AND guest_key_proof=$3
			RETURNING issued_at,expires_at`, nonce, launch.operation, fingerprint).Scan(&issued, &expiry)
		if err != nil {
			return denyMissing(err)
		}
		if err := checkEC2Challenge(issued, expiry, now); err != nil {
			return err
		}
		if launch.state == "active" {
			var encoded, deadline string
			err := tx.QueryRowContext(ctx, `SELECT certificate_der,expires_at FROM enrollment_grants
				WHERE operation_id=$1 AND provider_instance_id=$2 AND key_proof=$3 AND state='consumed'
				AND EXISTS(SELECT 1 FROM guest_registrations WHERE workspace_id=$4 AND generation=$5
					AND public_key_fingerprint=$3 AND last_authenticated_at IS NULL)`,
				launch.operation, launch.ref.InstanceID, fingerprint, launch.ref.WorkspaceID, launch.ref.Generation).Scan(&encoded, &deadline)
			if err != nil {
				return denyMissing(err)
			}
			der, err := hex.DecodeString(encoded)
			until, timeErr := time.Parse(time.RFC3339Nano, deadline)
			if err != nil || timeErr != nil || !now.Before(until) {
				return ErrEnrollmentDenied
			}
			response.CertificatePEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ec2_launch_enrollment SET guest_key_proof=$2 WHERE operation_id=$1`, launch.operation, fingerprint); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE enrollment_grants SET state='invalidated'
			WHERE workspace_id=$1 AND generation=$2 AND state='issued'`, launch.ref.WorkspaceID, launch.ref.Generation); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO enrollment_grants
			(id,workspace_id,generation,operation_id,provider_instance_id,expires_at,state,key_proof)
			VALUES($1,$2,$3,$4,$5,$6,'issued',$7)`, auth.TokenDigest(token), launch.ref.WorkspaceID, launch.ref.Generation,
			launch.operation, launch.ref.InstanceID, now.UTC().Add(grantLifetime).Format(time.RFC3339Nano), fingerprint)
		response.Grant = token
		return err
	})
	if err != nil {
		return EC2EnrollmentGrant{}, err
	}
	return response, nil
}

func (b *EC2Bootstrap) target(ctx context.Context, db enrollmentQuery, reference, fingerprint string) (ec2BootstrapLaunch, error) {
	var launch ec2BootstrapLaunch
	var workspace string
	var generation int64
	err := db.QueryRowContext(ctx, `SELECT o.id,o.workspace_id,o.generation,w.platform_artifact_id,COALESCE(b.guest_key_proof,'')
		FROM ec2_launch_enrollment b JOIN operations o ON o.id=b.operation_id
		JOIN workspaces w ON w.id=o.workspace_id AND w.generation=o.generation
		JOIN platform_artifacts a ON a.id=w.platform_artifact_id
		JOIN operation_steps s ON s.operation_id=o.id AND s.step_key=o.current_step
		WHERE b.launch_reference=$1 AND b.public_data IS NOT NULL AND o.type='provision' AND o.originating_action='workspace.launch'
		AND o.current_step='enroll' AND s.status='active' AND a.provider_kind='ec2' AND w.deletion_commit_at IS NULL`, reference).
		Scan(&launch.operation, &workspace, &generation, &launch.artifact, &launch.boundKey)
	if err != nil {
		return launch, denyMissing(err)
	}
	if launch.boundKey != "" && launch.boundKey != fingerprint {
		return launch, ErrEnrollmentDenied
	}
	launch.ref, launch.state, _, _, err = enrollmentTarget(ctx, db, workspace, generation, launch.operation)
	if err != nil {
		return launch, err
	}
	if launch.state != "eligible" && !(launch.state == "active" && launch.boundKey == fingerprint) {
		return launch, ErrEnrollmentDenied
	}
	return launch, nil
}

func checkEC2Challenge(issued, expiry string, now time.Time) error {
	start, startErr := time.Parse(time.RFC3339Nano, issued)
	end, endErr := time.Parse(time.RFC3339Nano, expiry)
	if startErr != nil || endErr != nil || now.Before(start) || !now.Before(end) {
		return ErrEnrollmentDenied
	}
	return nil
}

func ec2ProofContext(reference, nonce, fingerprint string, launch ec2BootstrapLaunch) string {
	fields := []string{"ShiftBox EC2 enrollment v1", launch.ref.InstallationID, launch.ref.WorkspaceID,
		strconv.FormatInt(launch.ref.Generation, 10), launch.operation, launch.ref.InstanceID, reference, nonce, fingerprint}
	digest := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return hex.EncodeToString(digest[:])
}
