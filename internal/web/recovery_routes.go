package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/jflatten/ShiftBox/internal/auth"
	"github.com/jflatten/ShiftBox/internal/backup"
	"github.com/jflatten/ShiftBox/internal/operation"
)

// RecoveryRoutes serves the one current review on the restricted listener.
func RecoveryRoutes(db *sql.DB, journal *operation.Journal, origin, reviewID string, now func() time.Time,
	observe func(context.Context) ([]backup.RecoveryWorkspace, error), activated func()) []*AuthorizedHandler {
	build := func(method, path, id, action string) *AuthorizedHandler {
		return &AuthorizedHandler{
			DB: db, Origin: origin, Now: now,
			Route: ManagementRoute{Method: method, Path: path, OperationID: id, Action: action},
			Resolve: func(_ context.Context, _ *sql.Tx, r *http.Request, _ auth.Principal) (AuthorizationTarget, error) {
				if r.PathValue("reviewId") != reviewID {
					return AuthorizationTarget{}, Problem{Status: 404, Code: "recovery.not_found", Title: "Recovery review not found"}
				}
				return AuthorizationTarget{Action: action, Resource: auth.Resource{Type: "recovery", ID: reviewID}}, nil
			},
		}
	}
	const path = "/recovery/{reviewId}"
	get := build(http.MethodGet, path, "getRecoveryReview", "recovery.review")
	get.Apply = func(ctx context.Context, tx *sql.Tx, _ *http.Request, _ auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		review, err := backup.ReadRecoveryReview(ctx, tx, reviewID)
		return noticeJSON(http.StatusOK, review, ""), err
	}
	update := build(http.MethodPatch, path, "updateRecoveryReview", "recovery.review")
	update.Apply = func(ctx context.Context, tx *sql.Tx, r *http.Request, actor auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		version, err := RequireVersion(r)
		if err != nil {
			return nil, err
		}
		var input struct {
			Section   string `json:"section"`
			Decisions struct {
				UserIDs         []string `json:"userIds"`
				AcceptRetention bool     `json:"acceptRetention"`
			} `json:"decisions"`
		}
		if err := DecodeJSON(nil, r, &input, 64<<10); err != nil {
			return nil, err
		}
		decision := backup.RecoveryDecisions{Section: input.Section, Version: version,
			UserIDs: input.Decisions.UserIDs, AcceptRetention: input.Decisions.AcceptRetention}
		if decision.Section == "provider" {
			return afterRecoveryObservation(update, observe, func(ctx context.Context, tx *sql.Tx, actor auth.Principal, evidence []backup.RecoveryWorkspace) (http.Handler, error) {
				decision.Workspaces = evidence
				return saveRecoveryReview(ctx, tx, reviewID, actor.User.ID, decision, now())
			}), nil
		}
		return saveRecoveryReview(ctx, tx, reviewID, actor.User.ID, decision, now())
	}
	activate := build(http.MethodPost, path+"/activate", "activateRecovery", "recovery.activate")
	activate.ReviewReady = func(ctx context.Context, tx *sql.Tx) (bool, error) {
		review, err := backup.ReadRecoveryReview(ctx, tx, reviewID)
		return review.State == "ready", err
	}
	activate.Apply = func(ctx context.Context, tx *sql.Tx, r *http.Request, _ auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
		key, err := IdempotencyKey(r)
		if err != nil {
			return nil, err
		}
		return afterRecoveryObservation(activate, observe, func(ctx context.Context, tx *sql.Tx, actor auth.Principal, evidence []backup.RecoveryWorkspace) (http.Handler, error) {
			op, err := backup.ActivateRecoveryInTx(ctx, tx, journal, reviewID, actor.User.ID, key, evidence, now())
			if err != nil {
				return nil, recoveryReviewError(err)
			}
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ClearSessionCookie(w)
				noticeJSON(http.StatusAccepted, op, "").ServeHTTP(w, r)
				if activated != nil {
					activated()
				}
			}), nil
		}), nil
	}
	return []*AuthorizedHandler{get, update, activate}
}

func saveRecoveryReview(ctx context.Context, tx *sql.Tx, reviewID, actor string, decision backup.RecoveryDecisions, now time.Time) (http.Handler, error) {
	review, err := backup.UpdateRecoveryReview(ctx, tx, reviewID, actor, decision, now)
	if err != nil {
		return nil, recoveryReviewError(err)
	}
	return noticeJSON(http.StatusOK, review, ""), nil
}

// Authorize again after the provider call, outside both SQL transactions.
func afterRecoveryObservation(handler *AuthorizedHandler, observe func(context.Context) ([]backup.RecoveryWorkspace, error),
	apply func(context.Context, *sql.Tx, auth.Principal, []backup.RecoveryWorkspace) (http.Handler, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if observe == nil {
			WriteProblem(w, RequestID(r.Context()), Problem{Status: 503, Code: "recovery.provider_unavailable", Title: "Provider inventory could not be verified"})
			return
		}
		inspect, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		evidence, err := observe(inspect)
		cancel()
		if err != nil {
			WriteProblem(w, RequestID(r.Context()), Problem{Status: 503, Code: "recovery.provider_unavailable", Title: "Provider inventory could not be verified"})
			return
		}
		current := *handler
		current.Apply = func(ctx context.Context, tx *sql.Tx, _ *http.Request, actor auth.Principal, _ AuthorizationTarget, _ auth.Decision) (http.Handler, error) {
			return apply(ctx, tx, actor, evidence)
		}
		current.ServeHTTP(w, r)
	})
}

func recoveryReviewError(err error) error {
	if errors.Is(err, backup.ErrRecoveryReview) || errors.Is(err, operation.ErrConflict) {
		return Problem{Status: 409, Code: "recovery.review_conflict", Title: "Refresh the review and resolve any incomplete checks"}
	}
	return err
}
