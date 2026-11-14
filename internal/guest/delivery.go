package guest

import (
	"context"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/provider"
)

// Deliver mints a grant and attaches its private ISO to the verified stopped
// VM. The caller retains the returned path until enrollment is consumed or
// explicitly invalidated. A failed delivery invalidates the grant before
// removing the image.
func (e *Enrollment) Deliver(ctx context.Context, operationID string, inventory provider.Inventory,
	now time.Time, root, endpoint string,
	attach func(context.Context, provider.EnrollmentRequest) (provider.DeliveryResult, error),
) (string, error) {
	if e.authority == nil || attach == nil || now.Before(e.authority.Certificate.NotBefore) ||
		!now.Before(e.authority.Certificate.NotAfter) {
		return "", ErrEnrollmentDenied
	}
	token, err := e.Mint(ctx, operationID, inventory, now)
	if err != nil {
		return "", err
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: e.authority.Certificate.Raw})
	image, err := BuildEnrollmentMedia(ctx, root, EnrollmentSeed{Grant: token, Endpoint: endpoint, CAPEM: string(ca)})
	if err == nil {
		result, attachErr := attach(ctx, provider.EnrollmentRequest{
			Ref: inventory.Ref, OperationID: operationID, MediaPath: image, Eligible: true,
		})
		switch {
		case attachErr != nil:
			err = attachErr
		case result.MediaID == "" || result.Inventory.Ref != inventory.Ref ||
			result.Inventory.InstanceID != inventory.InstanceID || result.Inventory.Power != provider.Stopped:
			err = provider.ErrOutcomeUnknown
		default:
			return image, nil
		}
	}

	// Attachment may have succeeded even when its response was lost.
	// Invalidate the unused grant even if the worker context was cancelled.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	result, cleanupErr := e.db.ExecContext(cleanup, `UPDATE enrollment_grants SET state='invalidated'
		WHERE id=$1 AND state='issued'`, auth.TokenDigest(token))
	if cleanupErr == nil {
		var changed int64
		changed, cleanupErr = result.RowsAffected()
		if cleanupErr == nil && changed != 1 {
			cleanupErr = provider.ErrOutcomeUnknown
		}
	}
	if cleanupErr != nil {
		return image, errors.Join(err, cleanupErr)
	}
	if image != "" {
		if cleanupErr := os.RemoveAll(filepath.Dir(image)); cleanupErr != nil {
			return image, errors.Join(err, errors.New("cannot remove invalidated enrollment media"))
		}
	}
	return "", err
}
