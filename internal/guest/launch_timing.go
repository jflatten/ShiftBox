package guest

import (
	"bytes"
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jflatten/ShiftBox/internal/database"
	"github.com/jflatten/ShiftBox/internal/launchlog"
)

// LaunchTimingOfferHeader advertises optional timing uploads to compatible agents.
const LaunchTimingOfferHeader = "ShiftBox-Launch-Timing"

// DetailedConfigureTimingHeader advertises support for additional guest phases.
const DetailedConfigureTimingHeader = "ShiftBox-Configure-Timing"

type launchTimingReport struct {
	OperationID     string                `json:"operationId"`
	DispatchVersion int64                 `json:"dispatchVersion"`
	Sample          launchlog.GuestSample `json:"sample"`
}

// RecordLaunchTiming verifies current guest authority and persists one sample
// without letting the guest choose the workspace or generation.
func (e *Enrollment) RecordLaunchTiming(ctx context.Context, leaf *x509.Certificate, now time.Time,
	operationID string, dispatchVersion int64, sample launchlog.GuestSample,
) error {
	return database.Transaction(ctx, e.db, func(tx *sql.Tx) error {
		contact, err := e.authenticate(ctx, tx, leaf, now, false)
		if err != nil {
			return err
		}
		if contact.DesiredState != "running" {
			return ErrGuestUnauthorized
		}
		return launchlog.RecordGuest(ctx, tx, contact.WorkspaceID, contact.Generation, operationID, dispatchVersion, sample, now)
	})
}

func (d devContainer) uploadLaunchTiming(ctx context.Context, dispatch guestDispatch, phase string, elapsed time.Duration, outcome string) {
	if !d.detailedConfigureTiming {
		switch phase {
		case "initial_devcontainer_setup", "recreate_with_retained_home":
			phase = "devcontainer_up"
		case "image_configuration", "image_prefetch", "ide_settings", "ide_extensions":
			return
		}
	}
	report := launchTimingReport{OperationID: dispatch.ID, DispatchVersion: dispatch.Version,
		Sample: launchlog.GuestSample{ID: uuid.NewString(), Phase: phase, Outcome: outcome, DurationMS: elapsed.Milliseconds()}}
	select {
	case d.launchTiming <- report:
	default:
		slog.WarnContext(ctx, "Launch timing upload queue is full", "operation_id", dispatch.ID)
	}
}

func runLaunchTiming(ctx context.Context, client *http.Client, endpoint string, queue <-chan launchTimingReport, retryDelay time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case report, ok := <-queue:
			if !ok {
				return
			}
			if !sendLaunchTiming(ctx, client, endpoint, report, retryDelay) && ctx.Err() == nil {
				slog.WarnContext(ctx, "Could not upload launch timing", "operation_id", report.OperationID, "phase", report.Sample.Phase)
			}
		}
	}
}

func sendLaunchTiming(ctx context.Context, client *http.Client, endpoint string, report launchTimingReport, retryDelay time.Duration) bool {
	return sendGuestDiagnostic(ctx, client, endpoint, report, retryDelay)
}

func sendGuestDiagnostic(ctx context.Context, client *http.Client, endpoint string, report any, retryDelay time.Duration) bool {
	payload, err := json.Marshal(report)
	if err != nil {
		return false
	}
	for attempt := range 3 {
		if attempt > 0 {
			timer := time.NewTimer(retryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return false
			case <-timer.C:
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return false
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err == nil {
			response.Body.Close()
			switch {
			case response.StatusCode == http.StatusNoContent:
				return true
			case response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests:
				return false
			}
		}
	}
	return false
}

func launchTimingEndpoint(pollEndpoint string) string {
	return guestDiagnosticEndpoint(pollEndpoint, launchlog.GuestPath)
}

func guestDiagnosticEndpoint(pollEndpoint, path string) string {
	if !validPollEndpoint(pollEndpoint) {
		return ""
	}
	target, _ := url.Parse(pollEndpoint)
	target.Path, target.RawPath = path, ""
	return target.String()
}
