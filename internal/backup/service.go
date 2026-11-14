package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/operation"
)

const (
	backupAction = "backup.create"
	backupLease  = 30 * time.Minute
)

// Service tracks operator-driven backup operations. Secrets are held only
// by the caller for the duration of a single attempt.
type Service struct {
	db       *sql.DB
	journal  *operation.Journal
	metadata ArchiveMetadata
}

// NewService binds backups to the active installation and schema.
func NewService(db *sql.DB, journal *operation.Journal, metadata ArchiveMetadata) *Service {
	return &Service{db: db, journal: journal, metadata: metadata}
}

// Admit creates the backup record in the caller's authorization transaction.
func (s *Service) Admit(ctx context.Context, tx *sql.Tx, actor, key string) (operation.Operation, error) {
	if !s.metadata.valid() {
		return operation.Operation{}, errArchiveInvalid
	}
	op, err := s.journal.CreateInTx(ctx, tx, operation.Request{
		Actor: actor, Key: key, Type: "backup", Generation: 1, OriginatingAction: backupAction,
		Steps: []operation.Step{
			{Key: "checkpoint", Label: "Capture management and identity data"},
			{Key: "encrypt", Label: "Encrypt and verify the archive"},
		},
	})
	if err != nil {
		return operation.Operation{}, err
	}
	if op.Type != "backup" || op.OriginatingAction != backupAction || op.WorkspaceID != nil {
		return operation.Operation{}, operation.ErrConflict
	}

	metadata, err := json.Marshal(s.metadata)
	if err != nil {
		return operation.Operation{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO backup_records(id,operation_id,format_version,product_release,schema_versions_json,state,created_by,created_at)
		VALUES($1,$1,$2,$3,$4,'preparing',$5,$6) ON CONFLICT(operation_id) DO NOTHING`,
		op.ID, archiveVersion, s.metadata.Release, string(metadata), actor, op.CreatedAt.Format(time.RFC3339Nano))
	return op, err
}

// Run captures and verifies one admitted backup. An incomplete attempt never
// becomes eligible for migration, even if it published an encrypted file.
func (s *Service) Run(ctx context.Context, id, output, password string, capture func(context.Context, string) (map[string]string, error)) (ArchiveEvidence, error) {
	if capture == nil {
		return ArchiveEvidence{}, errArchiveInvalid
	}
	var preparing bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM backup_records b JOIN operations o ON o.id=b.operation_id
		WHERE b.operation_id=$1 AND b.state='preparing' AND o.originating_action=$2)`, id, backupAction).Scan(&preparing)
	if err != nil {
		return ArchiveEvidence{}, err
	}
	if !preparing {
		return ArchiveEvidence{}, operation.ErrConflict
	}
	claim, err := s.journal.Claim(ctx, id, "management-backup", backupLease)
	if err != nil {
		return ArchiveEvidence{}, err
	}
	if err := s.transition(ctx, claim, 0, "preparing", "checkpointing", ""); err != nil {
		return ArchiveEvidence{}, err
	}
	effectCtx, cancel := context.WithTimeout(ctx, backupLease-time.Minute)
	defer cancel()
	step := 0
	evidence, err := CaptureArchive(effectCtx, output, password, s.metadata, func(ctx context.Context, dir string) (map[string]string, error) {
		sources, err := capture(ctx, dir)
		if err != nil {
			return nil, err
		}
		if err := s.transition(ctx, claim, 0, "checkpointing", "encrypting", "Management and identity checkpoints captured."); err != nil {
			return nil, err
		}
		step = 1
		return sources, nil
	})
	if err != nil {
		// Cancellation of the request must still leave a durable failure.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return ArchiveEvidence{}, s.recordFailure(cleanup, claim, step)
	}
	// After the archive has been published, finalize even if the request ends.
	finishCtx, finish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finish()
	err = database.Transaction(finishCtx, s.db, func(tx *sql.Tx) error {
		if err := s.journal.CompleteStepInTx(finishCtx, tx, claim, 1, "Encrypted archive closed and verified."); err != nil {
			return err
		}
		result, err := tx.ExecContext(finishCtx, `UPDATE backup_records SET state='completed', manifest_digest=$1,
			archive_digest=$2, size_bytes=$3, completed_at=$4 WHERE operation_id=$5 AND state='encrypting'`,
			evidence.ManifestDigest, evidence.ArchiveDigest, evidence.Size, time.Now().UTC().Format(time.RFC3339Nano), claim.OperationID)
		return changedRecord(result, err)
	})
	if err != nil {
		return ArchiveEvidence{}, errors.New("encrypted archive was published but its completion could not be recorded")
	}
	return evidence, nil
}

func (s *Service) transition(ctx context.Context, claim operation.Claim, step int, from, to, detail string) error {
	return database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		if detail != "" {
			if err := s.journal.CompleteStepInTx(ctx, tx, claim, step, detail); err != nil {
				return err
			}
			step++
		}
		if err := s.journal.StartStepInTx(ctx, tx, claim, step); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE backup_records SET state=$1 WHERE operation_id=$2 AND state=$3",
			to, claim.OperationID, from)
		return changedRecord(result, err)
	})
}

func (s *Service) recordFailure(ctx context.Context, claim operation.Claim, step int) error {
	code, detail := "backup.checkpoint_failed", "Could not capture the management and identity checkpoint."
	if step == 1 {
		code, detail = "backup.archive_failed", "Could not publish a verified encrypted archive."
	}
	err := database.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		if err := s.journal.FailStepInTx(ctx, tx, claim, step, detail); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE backup_records SET state='failed', error_code=$1 WHERE operation_id=$2
			AND state IN ('checkpointing','encrypting')`, code, claim.OperationID)
		return changedRecord(result, err)
	})
	if err != nil {
		return errors.New("could not save failed backup operation")
	}
	return errors.New(detail)
}

func changedRecord(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return operation.ErrConflict
	}
	return nil
}

// HasCompletedBackup reads durable verification evidence for this exact
// installation, deployed release, and application schema.
func HasCompletedBackup(ctx context.Context, db *sql.DB, metadata ArchiveMetadata) (bool, error) {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return false, err
	}
	var found bool
	err = db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM backup_records b JOIN operations o ON o.id=b.operation_id
		WHERE b.state='completed' AND b.product_release=$1 AND b.format_version=$2 AND b.schema_versions_json=$3
		  AND o.status='succeeded' AND o.type='backup' AND o.originating_action=$4)`,
		metadata.Release, archiveVersion, string(raw), backupAction).Scan(&found)
	return found, err
}
