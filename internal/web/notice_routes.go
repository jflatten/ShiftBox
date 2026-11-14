package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jflatten/ShiftBox/internal/auth"
)

const noticeTime = "2006-01-02T15:04:05.000000000Z"

type noticeInput struct {
	Severity    string     `json:"severity"`
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	EffectiveAt *time.Time `json:"effectiveAt"`
	ExpiresAt   *time.Time `json:"expiresAt"`
}

type noticeView struct {
	noticeInput
	ID          string    `json:"id"`
	CreatedAt   time.Time `json:"createdAt"`
	ReadAt      *string   `json:"readAt"`
	DismissedAt *string   `json:"dismissedAt"`
}

// NoticeHandlers serves systemwide notices and receipts scoped to the current user.
func NoticeHandlers(db *sql.DB, origin string, now func() time.Time, newID func() string) (AuthorizedHandler, AuthorizedHandler, AuthorizedHandler) {
	base := AuthorizedHandler{DB: db, Origin: origin, Now: now}
	publish := base
	publish.Route = ManagementRoute{Method: http.MethodPost, Path: "/admin/notices", OperationID: "publishNotice", Action: "notice.publish"}
	publish.Resolve = func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
		return AuthorizationTarget{Action: "notice.publish"}, nil
	}
	publish.Apply = func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		return publishNotice(ctx, tx, r, actor.User.ID, now().UTC(), newID)
	}
	list := base
	list.Route = ManagementRoute{Method: http.MethodGet, Path: "/notices", OperationID: "listNotices", Action: "notice.list"}
	list.Resolve = func(context.Context, *sql.Tx, *http.Request, auth.Principal) (AuthorizationTarget, error) {
		return AuthorizationTarget{Action: "notice.list"}, nil
	}
	list.Apply = func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		return listNotices(ctx, tx, r, actor.User.ID, now().UTC())
	}
	receipt := base
	receipt.Route = ManagementRoute{Method: http.MethodPatch, Path: "/notices/{noticeId}/receipt", OperationID: "updateNoticeReceipt", Resolver: "notice_receipt"}
	receipt.Resolve = func(_ context.Context, _ *sql.Tx, r *http.Request, _ auth.Principal) (AuthorizationTarget, error) {
		id := r.PathValue("noticeId")
		if !ValidUUID(id) {
			return AuthorizationTarget{}, invalidAuthorizationFields()
		}
		var input struct {
			State string `json:"state"`
		}
		if err := DecodeJSON(nil, r, &input, 1024); err != nil {
			return AuthorizationTarget{}, err
		}
		return NoticeReceiptTarget(input.State, id)
	}
	receipt.Apply = func(ctx context.Context, tx *sql.Tx, _ *http.Request, actor auth.Principal, target AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		return saveNoticeReceipt(ctx, tx, actor.User.ID, target, now().UTC())
	}
	return publish, list, receipt
}

func publishNotice(ctx context.Context, tx *sql.Tx, r *http.Request, actor string, now time.Time, newID func() string) (http.Handler, error) {
	var input noticeInput
	if err := DecodeJSON(nil, r, &input, 32<<10); err != nil {
		return nil, err
	}
	input.Title, input.Body = strings.TrimSpace(input.Title), strings.TrimSpace(input.Body)
	if input.EffectiveAt == nil {
		input.EffectiveAt = &now
	}
	if !validNotice(input) {
		return nil, Problem{Status: 400, Code: "notice.invalid_input", Title: "Check the notice text, severity, and dates"}
	}
	if newID == nil {
		return nil, errors.New("notice identity generator unavailable")
	}
	id := newID()
	var expires any
	if input.ExpiresAt != nil {
		expires = input.ExpiresAt.UTC().Format(noticeTime)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO notices(id,severity,title,body,effective_at,expires_at,created_by,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, input.Severity, input.Title, input.Body,
		input.EffectiveAt.UTC().Format(noticeTime), expires, actor, now.Format(noticeTime))
	if err != nil {
		return nil, err
	}
	return noticeJSON(http.StatusCreated, noticeView{noticeInput: input, ID: id, CreatedAt: now}, ""), nil
}

func validNotice(input noticeInput) bool {
	switch input.Severity {
	case "info", "warning", "critical":
	default:
		return false
	}
	return input.Title != "" && input.Body != "" && utf8.RuneCountInString(input.Title) <= 120 &&
		utf8.RuneCountInString(input.Body) <= 4000 && !strings.ContainsRune(input.Title, 0) && !strings.ContainsRune(input.Body, 0) &&
		(input.ExpiresAt == nil || input.ExpiresAt.After(*input.EffectiveAt))
}

func listNotices(ctx context.Context, tx *sql.Tx, r *http.Request, user string, now time.Time) (http.Handler, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, invalidAuthorizationFields()
	}
	history, cursor := query.Get("includeDismissed"), query.Get("cursor")
	if (history != "" && history != "true" && history != "false") || (cursor != "" && !ValidUUID(cursor)) {
		return nil, invalidAuthorizationFields()
	}
	const limit = 50
	rows, err := tx.QueryContext(ctx, `SELECT n.id,n.severity,n.title,n.body,n.effective_at,n.expires_at,n.created_at,r.read_at,r.dismissed_at
		FROM notices n LEFT JOIN notice_receipts r ON n.id=r.notice_id AND r.user_id=$1
		WHERE n.id>$2 AND n.effective_at<=$3 AND (($4 AND r.read_at IS NOT NULL) OR
		 ((n.expires_at IS NULL OR n.expires_at>$3) AND r.dismissed_at IS NULL))
		ORDER BY n.id LIMIT $5`, user, cursor, now.Format(noticeTime), history == "true", limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]noticeView, 0, limit)
	var next string
	for rows.Next() {
		if len(items) == limit {
			values := url.Values{"cursor": {items[len(items)-1].ID}}
			if history == "true" {
				values.Set("includeDismissed", history)
			}
			next = "</api/v1/notices?" + values.Encode() + `>; rel="next"`
			break
		}
		var item noticeView
		var effective, created string
		var expires sql.NullString
		if err := rows.Scan(&item.ID, &item.Severity, &item.Title, &item.Body, &effective, &expires, &created, &item.ReadAt, &item.DismissedAt); err != nil {
			return nil, err
		}
		at, err := time.Parse(time.RFC3339Nano, effective)
		if err != nil {
			return nil, err
		}
		item.EffectiveAt = &at
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		if expires.Valid {
			at, err := time.Parse(time.RFC3339Nano, expires.String)
			if err != nil {
				return nil, err
			}
			item.ExpiresAt = &at
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return noticeJSON(http.StatusOK, items, next), nil
}

func saveNoticeReceipt(ctx context.Context, tx *sql.Tx, user string, target AuthorizationTarget, now time.Time) (http.Handler, error) {
	id, stamp := target.Resource.ID, now.Format(noticeTime)
	var visible bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM notices
		WHERE id=$1 AND effective_at<=$2 AND (expires_at IS NULL OR expires_at>$2))`, id, stamp).Scan(&visible)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, Problem{Status: 404, Code: "notice.not_found", Title: "Notice unavailable"}
	}
	if target.Action == "notice.read" {
		_, err = tx.ExecContext(ctx, `INSERT INTO notice_receipts(notice_id,user_id,read_at)
			VALUES($1,$2,$3) ON CONFLICT(notice_id,user_id) DO NOTHING`, id, user, stamp)
	} else {
		var result sql.Result
		result, err = tx.ExecContext(ctx, `UPDATE notice_receipts SET dismissed_at=COALESCE(dismissed_at,$3)
			WHERE notice_id=$1 AND user_id=$2`, id, user, stamp)
		if err == nil {
			var count int64
			count, err = result.RowsAffected()
			if err == nil && count == 0 {
				return nil, Problem{Status: 409, Code: "notice.read_required", Title: "Mark the notice as read before dismissing it"}
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), nil
}

func noticeJSON(status int, value any, link string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if link != "" {
			w.Header().Set("Link", link)
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
	})
}
