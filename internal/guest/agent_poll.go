package guest

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/jflatten/ShiftBox/internal/workspace"
)

const pollPath = "/internal/v1/guest/poll"

var errAgentOperationPending = errors.New("guest operation execution unavailable")

// Poll authenticates a single poll using the saved guest identity.
func (identity AgentIdentity) Poll(ctx context.Context, endpoint, serverName, protocol, release string) error {
	client := identity.pollClient(serverName)
	defer client.CloseIdleConnections()
	return pollAgent(ctx, client, endpoint, newGuestPollRequest(protocol, release, nil), nil, devContainer{}, nil)
}

func (identity AgentIdentity) pollClient(serverName string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13, RootCAs: identity.Roots,
			Certificates: []tls.Certificate{identity.Certificate}, ServerName: serverName,
		}},
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type guestPollRequest struct {
	ProtocolVersion      string                 `json:"protocolVersion"`
	GuestRelease         string                 `json:"guestRelease"`
	LastOperationID      string                 `json:"lastOperationId,omitempty"`
	LastOperationVersion int64                  `json:"lastOperationVersion,omitempty"`
	Report               *OperationReport       `json:"operationReport,omitempty"`
	Activity             *workspace.CPUActivity `json:"activity,omitempty"`
}

func newGuestPollRequest(protocol, release string, journal *localJournal) guestPollRequest {
	input := guestPollRequest{ProtocolVersion: protocol, GuestRelease: release}
	if journal == nil || journal.state.Operation == nil {
		return input
	}
	op := journal.state.Operation
	switch op.Phase {
	case localWaitReached, localSucceeded, localFailed:
		report := journal.report()
		input.LastOperationID = report.ID
		input.LastOperationVersion = op.Version
		input.Report = &report
	}
	return input
}

func pollAgent(ctx context.Context, client *http.Client, endpoint string, input guestPollRequest, journal *localJournal, runtime devContainer, tunnel *agentTunnel) error {
	if !validPollEndpoint(endpoint) || input.ProtocolVersion == "" || input.GuestRelease == "" {
		return errors.New("invalid guest poll configuration")
	}
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid guest poll request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("guest poll HTTPS exchange failed")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusUnauthorized:
		return ErrGuestUnauthorized
	default:
		return errors.New("controller did not accept guest poll")
	}
	var result struct {
		ProtocolVersion string          `json:"protocolVersion"`
		ServerTime      time.Time       `json:"serverTime"`
		Generation      int64           `json:"workspaceGeneration"`
		DesiredState    string          `json:"desiredState"`
		Operation       json.RawMessage `json:"operation"`
		Certificate     struct {
			RenewAfter time.Time `json:"renewAfter"`
		} `json:"certificate"`
		NextPollSeconds int `json:"nextPollAfterSeconds"`
	}
	if err := decodeAgentJSON(response.Body, &result); err != nil || result.ProtocolVersion != input.ProtocolVersion ||
		result.Generation < 1 || result.ServerTime.IsZero() || result.Certificate.RenewAfter.IsZero() {
		return errors.New("invalid guest poll response")
	}
	if tunnel != nil {
		offer, err := readTunnelOffer(endpoint, response.Header, result.Generation, result.DesiredState)
		if err != nil {
			return err
		}
		if result.Generation < journal.state.Generation {
			return errGuestDispatchConflict
		}
		tunnel.update(ctx, offer)
	}
	return applyGuestPoll(ctx, journal, runtime, result.Generation, result.DesiredState, result.Operation)
}

func applyGuestPoll(ctx context.Context, journal *localJournal, runtime devContainer, generation int64, desiredState string, raw json.RawMessage) error {
	hasOperation := len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	if journal == nil {
		if hasOperation {
			return errAgentOperationPending
		}
		return nil
	}
	if err := journal.advanceGeneration(generation); err != nil {
		return err
	}
	if !hasOperation {
		return nil
	}
	var dispatch struct {
		ID        string `json:"id"`
		Version   int64  `json:"version"`
		Type      string `json:"type"`
		Command   string `json:"command"`
		Arguments struct {
			RepositoryURL  string            `json:"repositoryUrl"`
			RepositoryRef  string            `json:"repositoryRef"`
			Devcontainer   string            `json:"devcontainerPath"`
			RetainedMounts []json.RawMessage `json:"retainedMounts"`
		} `json:"arguments"`
	}
	if err := decodeAgentJSON(bytes.NewReader(raw), &dispatch); err != nil {
		return errors.New("invalid guest operation dispatch")
	}
	if dispatch.Command == enterRepairCommand || dispatch.Command == exitRepairCommand {
		if desiredState != "running" || dispatch.Type != "repair" || dispatch.Arguments.RepositoryURL != "" ||
			dispatch.Arguments.RepositoryRef != "" || dispatch.Arguments.Devcontainer != "" || len(dispatch.Arguments.RetainedMounts) != 0 {
			return errors.New("invalid repair dispatch")
		}
		return journal.repair(ctx, guestDispatch{
			ID: dispatch.ID, Version: dispatch.Version, Generation: generation, Command: dispatch.Command,
		}, runtime)
	}
	if dispatch.Command == "shutdown-for-stop" {
		if desiredState != "stopped" || dispatch.Type != "stop" || dispatch.Arguments.RepositoryURL != "" ||
			dispatch.Arguments.RepositoryRef != "" || dispatch.Arguments.Devcontainer != "" || len(dispatch.Arguments.RetainedMounts) != 0 {
			return errors.New("invalid workload shutdown dispatch")
		}
		_, err := journal.shutdown(ctx, guestDispatch{
			ID: dispatch.ID, Version: dispatch.Version, Generation: generation, Command: dispatch.Command,
		}, runtime)
		return err
	}
	if dispatch.Command != "ensure-development-container" && dispatch.Command != rebuildDevelopmentCommand || len(dispatch.Arguments.RetainedMounts) != 0 {
		return errAgentOperationPending
	}
	if desiredState != "running" || !validDevContainerPath(dispatch.Arguments.Devcontainer) ||
		(dispatch.Command == rebuildDevelopmentCommand) != (dispatch.Type == "rebuild") {
		return errors.New("invalid development container dispatch")
	}
	runtime.config = dispatch.Arguments.Devcontainer
	_, err := journal.ensureDevelopment(ctx, guestDispatch{
		ID: dispatch.ID, Version: dispatch.Version, Generation: generation, Command: dispatch.Command, Config: runtime.config,
		Repository: dispatch.Arguments.RepositoryURL, Ref: dispatch.Arguments.RepositoryRef,
	}, runtime)
	return err
}

func validPollEndpoint(endpoint string) bool {
	target, err := url.Parse(endpoint)
	return err == nil && target.Scheme == "https" && target.Hostname() != "" && target.Path == pollPath &&
		target.User == nil && target.RawQuery == "" && !target.ForceQuery && target.Fragment == ""
}

// ServeAgent loads the on-disk identity or enrolls from mounted recovery media,
// then maintains authenticated contact until stopped by the supervisor.
func ServeAgent(ctx context.Context, stateDir, seedPath, protocol, release, secretSocket string) error {
	if !filepath.IsAbs(stateDir) || !filepath.IsAbs(seedPath) {
		return errors.New("invalid guest service configuration")
	}
	identity, err := LoadAgentIdentity(stateDir, time.Now())
	if err != nil {
		seed, err := os.Open(seedPath)
		if err != nil {
			return errors.New("no valid guest identity or recovery media")
		}
		identity, err = EnrollAgent(ctx, stateDir, seed, protocol, release, time.Now())
		seed.Close()
		if err != nil {
			return err
		}
	}
	client := identity.pollClient("")
	defer client.CloseIdleConnections()
	if secretSocket != "" {
		stopSecrets, err := listenSecretSocket(ctx, secretSocket, client, identity.PollEndpoint)
		if err != nil {
			return err
		}
		defer stopSecrets()
	}
	tunnel := &agentTunnel{protocol: protocol, tls: client.Transport.(*http.Transport).TLSClientConfig.Clone(),
		retry: 5 * time.Second, serve: ServeTunnel}
	defer tunnel.close()
	journal, err := loadLocalJournal(filepath.Join(stateDir, "operation.json"))
	if err != nil {
		return err
	}
	project := filepath.Join(stateDir, "project")
	runtime := devContainer{workspace: project, credentials: secretSocket != "", ide: true, retainHome: true,
		repairImageFile: "/usr/local/share/shiftbox/repair-image", repairSocketDir: repairDirectory, repository: &repositoryPreparation{
			project: project,
			token: func(ctx context.Context) (string, error) {
				return guestGitAccessToken(ctx, client, identity.PollEndpoint)
			},
		}}
	var sampler cpuSampler
	for {
		input := newGuestPollRequest(protocol, release, journal)
		input.Activity = sampler.sample(time.Now())
		if err := pollAgent(ctx, client, identity.PollEndpoint, input, journal, runtime, tunnel); err != nil {
			return err
		}
		delay := 27*time.Second + rand.N(6*time.Second)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
