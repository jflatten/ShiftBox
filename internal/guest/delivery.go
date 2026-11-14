package guest

import (
	"context"
	"database/sql"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// Deliver records the recovery image before attaching it to the verified VM.
// A stopped retry can reuse an intact image after an interrupted attachment.
// Failed attachment invalidates the grant before removing the image.
func (e *Enrollment) Deliver(ctx context.Context, operationID string, inventory provider.Inventory,
	now time.Time, root, endpoint string,
	attach func(context.Context, provider.EnrollmentRequest) (provider.DeliveryResult, error),
) (string, error) {
	if e.authority == nil || attach == nil || !filepath.IsAbs(root) ||
		now.Before(e.authority.Certificate.NotBefore) || !now.Before(e.authority.Certificate.NotAfter) ||
		!stoppedForEnrollment(inventory, now) {
		return "", ErrEnrollmentDenied
	}
	id, directory, attached, err := e.pendingMedia(ctx, operationID, inventory.Ref, now)
	if err != nil {
		return "", err
	}

	var image string
	if directory != "" {
		image, err = enrollmentImagePath(root, directory)
		if err != nil {
			return "", err
		}
		info, statErr := os.Lstat(image)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return image, provider.ErrOutcomeUnknown
		}
		if attached {
			return image, nil
		}
	} else {
		token, err := e.Mint(ctx, operationID, inventory, now)
		if err != nil {
			return "", err
		}
		id = auth.TokenDigest(token)
		ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: e.authority.Certificate.Raw})
		image, err = BuildEnrollmentMedia(ctx, root, EnrollmentSeed{Grant: token, Endpoint: endpoint, CAPEM: string(ca)})
		if err != nil {
			return e.failDelivery(ctx, id, image, err)
		}
		directory = filepath.Base(filepath.Dir(image))
		result, err := e.db.ExecContext(ctx, `UPDATE enrollment_grants SET media_directory=$2
			WHERE id=$1 AND state='issued' AND media_directory IS NULL`, id, directory)
		if err := mediaWrite(result, err); err != nil {
			return e.failDelivery(ctx, id, image, err)
		}
	}
	result, err := attach(ctx, provider.EnrollmentRequest{
		Ref: inventory.Ref, OperationID: operationID, MediaPath: image, Eligible: true,
	})
	if err == nil && (result.MediaID == "" || result.Inventory.Ref != inventory.Ref ||
		result.Inventory.InstanceID != inventory.InstanceID || result.Inventory.Power != provider.Stopped) {
		err = provider.ErrOutcomeUnknown
	}
	if err != nil {
		return e.failDelivery(ctx, id, image, err)
	}
	updated, err := e.db.ExecContext(ctx, `UPDATE enrollment_grants SET media_attached_at=$2
		WHERE id=$1 AND state='issued' AND media_directory=$3`, id, now.UTC().Format(time.RFC3339Nano), directory)
	// An interrupted write keeps the prepared media available for a retry.
	return image, mediaWrite(updated, err)
}

func (e *Enrollment) pendingMedia(ctx context.Context, operationID string, ref provider.WorkspaceRef, now time.Time) (string, string, bool, error) {
	var id, directory, expires string
	var attached bool
	current, state, _, _, err := enrollmentTarget(ctx, e.db, ref.WorkspaceID, ref.Generation, operationID)
	if err != nil {
		return "", "", false, err
	}
	if current != ref || (state != "eligible" && state != "expired") {
		return "", "", false, ErrEnrollmentDenied
	}
	err = e.db.QueryRowContext(ctx, `SELECT id,media_directory,media_attached_at IS NOT NULL,expires_at
		FROM enrollment_grants WHERE workspace_id=$1 AND generation=$2 AND operation_id=$3 AND provider_instance_id=$4
		AND state='issued' AND media_directory IS NOT NULL`, ref.WorkspaceID, ref.Generation, operationID, ref.InstanceID).
		Scan(&id, &directory, &attached, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	deadline, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return "", "", false, err
	}
	if !now.Before(deadline) {
		return "", "", false, nil
	}
	return id, directory, attached, nil
}

func (e *Enrollment) failDelivery(ctx context.Context, id, image string, cause error) (string, error) {
	// The Apple event may have applied even when its response was lost.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	result, err := e.db.ExecContext(cleanup, `UPDATE enrollment_grants SET state='invalidated'
		WHERE id=$1 AND state='issued'`, id)
	if err := mediaWrite(result, err); err != nil {
		return image, errors.Join(cause, err)
	}
	if image != "" {
		if err := e.removeMedia(cleanup, id, image); err != nil {
			return image, errors.Join(cause, err)
		}
	}
	return "", cause
}

// CleanupDelivery removes tracked media for an operation after the caller
// observes successful authenticated guest contact. Invalidated attempts can
// also be removed here. Any still-issued grant remains available for boot.
func (e *Enrollment) CleanupDelivery(ctx context.Context, operationID string, ref provider.WorkspaceRef, root string) error {
	rows, err := e.db.QueryContext(ctx, `SELECT g.id,g.media_directory FROM enrollment_grants g
		JOIN workspaces w ON w.id=g.workspace_id
		WHERE g.operation_id=$1 AND g.workspace_id=$2 AND g.generation=$3 AND g.provider_instance_id=$4
		AND w.installation_id=$5 AND w.ownership_marker=$6 AND w.generation=$3 AND w.provider_instance_id=$4
		AND g.state IN ('consumed','invalidated','expired') AND g.media_directory IS NOT NULL`,
		operationID, ref.WorkspaceID, ref.Generation, ref.InstanceID, ref.InstallationID, ref.OwnershipMarker)
	if err != nil {
		return err
	}
	type media struct{ id, dir string }
	var paths []media
	for rows.Next() {
		var entry media
		if err := rows.Scan(&entry.id, &entry.dir); err != nil {
			rows.Close()
			return err
		}
		paths = append(paths, entry)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, entry := range paths {
		image, err := enrollmentImagePath(root, entry.dir)
		if err != nil {
			return err
		}
		if err := e.removeMedia(ctx, entry.id, image); err != nil {
			return err
		}
	}
	return nil
}

func enrollmentImagePath(root, directory string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Base(directory) != directory || !strings.HasPrefix(directory, "enrollment-") {
		return "", ErrInvalidEnrollmentSeed
	}
	return filepath.Join(root, directory, "enrollment.iso"), nil
}

func (e *Enrollment) removeMedia(ctx context.Context, id, image string) error {
	if err := os.RemoveAll(filepath.Dir(image)); err != nil {
		return errors.New("cannot remove enrollment media")
	}
	_, err := e.db.ExecContext(ctx, `UPDATE enrollment_grants SET media_directory=NULL,media_attached_at=NULL
		WHERE id=$1 AND state IN ('consumed','invalidated','expired')`, id)
	return err
}

func mediaWrite(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return provider.ErrOutcomeUnknown
	}
	return nil
}
