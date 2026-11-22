package template

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jflatten/ShiftBox/internal/audit"
)

// GrantLaunch records an acknowledged grant and its request identity in the
// authority transaction so a retry cannot resurrect a revoked grant.
func GrantLaunch(ctx context.Context, tx *sql.Tx, id, user, actor, key string, now time.Time) error {
	// Serialize request keys with template creation and revisions. The template
	// lock also orders grant changes against archival and revocation.
	if _, err := tx.ExecContext(ctx, "UPDATE installations SET version=version WHERE singleton=1 AND state='active'"); err != nil {
		return err
	}
	if err := lockTemplate(ctx, tx, id); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte("grantTemplateLaunch:" + id + ":" + user))
	digest := hex.EncodeToString(sum[:])
	var saved string
	err := tx.QueryRowContext(ctx, "SELECT request_digest FROM template_requests WHERE actor_id=$1 AND request_key=$2", actor, key).Scan(&saved)
	if err == nil {
		if saved != digest {
			return ErrRequestConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var available bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM project_templates t CROSS JOIN users u
		WHERE t.id=$1 AND t.state='active' AND u.id=$2 AND u.membership_state='active')`, id, user).Scan(&available); err != nil {
		return err
	}
	if !available {
		return ErrUnavailable
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT INTO template_grants(template_id,user_id,can_launch,project_secret_warning_ack_at,granted_by,granted_at)
		VALUES($1,$2,1,$3,$4,$3) ON CONFLICT(template_id,user_id) WHERE revoked_at IS NULL DO NOTHING`, id, user, stamp, actor)
	if err != nil {
		return err
	}
	if err := auditGrantChange(ctx, tx, result, id, actor, "launch_granted", now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO template_requests(actor_id,request_key,request_digest,response_json) VALUES($1,$2,$3,'null')",
		actor, key, digest)
	return err
}

// RevokeLaunch ends future launch and project-secret access for this grant.
func RevokeLaunch(ctx context.Context, tx *sql.Tx, id, user, actor string, now time.Time) error {
	if err := lockTemplate(ctx, tx, id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE template_grants SET revoked_at=$3
		WHERE template_id=$1 AND user_id=$2 AND revoked_at IS NULL`, id, user, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return auditGrantChange(ctx, tx, result, id, actor, "launch_revoked", now)
}

func auditGrantChange(ctx context.Context, tx *sql.Tx, result sql.Result, id, actor, reason string, now time.Time) error {
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return err
	}
	_, err = audit.Append(ctx, tx, audit.Event{
		OccurredAt: now, ActorType: "user", ActorID: actor, Action: "template.share",
		ResourceType: "template", ResourceID: id, Outcome: "allowed", ReasonCode: reason,
	})
	return err
}

func lockTemplate(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, "UPDATE project_templates SET version=version WHERE id=$1", id)
	return err
}
