package guest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/jflatten/ShiftBox/internal/proxy"
	"github.com/jflatten/ShiftBox/internal/workspace"
)

const pollPath = "/internal/v1/guest/poll"

var errAgentOperationPending = errors.New("guest operation execution unavailable")
var errAgentIdentityRenewed = errors.New("guest identity renewed")

// Poll authenticates a single poll using the saved guest identity.
func (identity AgentIdentity) Poll(ctx context.Context, endpoint, serverName, protocol, release string) error {
	client := identity.pollClient(serverName)
	defer client.CloseIdleConnections()
	_, err := pollAgent(ctx, client, endpoint, newGuestPollRequest(protocol, release, nil), nil, devContainer{}, nil, nil)
	return err
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
	ProtocolVersion      string                     `json:"protocolVersion"`
	GuestRelease         string                     `json:"guestRelease"`
	LastOperationID      string                     `json:"lastOperationId,omitempty"`
	LastOperationVersion int64                      `json:"lastOperationVersion,omitempty"`
	Report               *OperationReport           `json:"operationReport,omitempty"`
	Activity             *workspace.CPUActivity     `json:"activity,omitempty"`
	WatchedProcesses     *workspace.ProcessActivity `json:"watchedProcesses,omitempty"`
	Probes               []workspace.ProbeResult    `json:"probes,omitempty"`
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

func pollAgent(ctx context.Context, client *http.Client, endpoint string, input guestPollRequest,
	journal *localJournal, runtime devContainer, tunnel *agentTunnel, probes *probeReporter,
) (time.Duration, error) {
	if !validPollEndpoint(endpoint) || input.ProtocolVersion == "" || input.GuestRelease == "" {
		return 0, errors.New("invalid guest poll configuration")
	}
	if probes != nil && probes.enabled {
		input.Probes = []workspace.ProbeResult{{ID: workspace.ProbeCodeServer, Status: probeCodeServer(ctx, probes.socket)}}
	}
	body, err := json.Marshal(input)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("invalid guest poll request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, errors.New("guest poll HTTPS exchange failed")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusUnauthorized:
		return 0, ErrGuestUnauthorized
	default:
		return 0, errors.New("controller did not accept guest poll")
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
		return 0, errors.New("invalid guest poll response")
	}
	if tunnel != nil {
		offer, err := readTunnelOffer(endpoint, response.Header, result.Generation, result.DesiredState)
		if err != nil {
			return 0, err
		}
		if result.Generation < journal.state.Generation {
			return 0, errGuestDispatchConflict
		}
		tunnel.update(ctx, offer)
	}
	if probes != nil {
		probes.enabled = result.DesiredState == "running" && response.Header.Get(ProbeReportsHeader) == "1"
	}
	if runtime.applyImageCache != nil {
		mode := response.Header.Get(ImageCacheOfferHeader)
		if err := runtime.applyImageCache(mode); err != nil {
			return 0, err
		}
		runtime.imageCache = mode != ""
	}
	if err := applyGuestPoll(ctx, journal, runtime, result.Generation, result.DesiredState, result.Operation); err != nil {
		return 0, err
	}
	// A new durable receipt is worth an immediate round trip. A replay of the
	// same phase keeps the normal interval, even if the controller redispatches.
	if journal != nil && journal.state.Operation != nil && journal.state.Operation.Command == ensureDevelopmentCommand {
		receipt := newGuestPollRequest(input.ProtocolVersion, input.GuestRelease, journal).Report
		if receipt != nil && (input.Report == nil || *input.Report != *receipt) {
			return 0, nil
		}
	}
	if result.NextPollSeconds == 1 {
		return time.Second + rand.N(250*time.Millisecond), nil
	}
	return 27*time.Second + rand.N(6*time.Second), nil
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
			ProductRelease string            `json:"productRelease"`
		} `json:"arguments"`
	}
	if err := decodeAgentJSON(bytes.NewReader(raw), &dispatch); err != nil {
		return errors.New("invalid guest operation dispatch")
	}
	if dispatch.Command == stageGuestUpdateCommand || dispatch.Command == activateGuestUpdateCommand {
		if dispatch.Type != "update" || desiredState != "running" && desiredState != "stopped" ||
			dispatch.Arguments.RepositoryURL != "" || dispatch.Arguments.RepositoryRef != "" ||
			dispatch.Arguments.Devcontainer != "" || len(dispatch.Arguments.RetainedMounts) != 0 {
			return errors.New("invalid guest update dispatch")
		}
		bound := guestDispatch{ID: dispatch.ID, Version: dispatch.Version, Generation: generation,
			Command: dispatch.Command, ProductRelease: dispatch.Arguments.ProductRelease}
		if bound.Command == activateGuestUpdateCommand {
			return journal.activateUpdate(bound)
		}
		return journal.stageUpdate(ctx, bound, journal.downloadUpdate)
	}
	if dispatch.Arguments.ProductRelease != "" {
		return errors.New("unexpected guest update release")
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
	replacement := dispatch.Command == rebuildDevelopmentCommand || dispatch.Command == retryAfterRepairCommand
	if dispatch.Command != ensureDevelopmentCommand && !replacement || len(dispatch.Arguments.RetainedMounts) != 0 {
		return errAgentOperationPending
	}
	if desiredState != "running" || !validDevContainerPath(dispatch.Arguments.Devcontainer) ||
		replacement != (dispatch.Type == "rebuild") {
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
	if !filepath.IsAbs(seedPath) {
		return errors.New("invalid guest service configuration")
	}
	return serveAgent(ctx, stateDir, protocol, release, secretSocket, func() (AgentIdentity, error) {
		seed, err := os.Open(seedPath)
		if err != nil {
			return AgentIdentity{}, errors.New("no valid guest identity or recovery media")
		}
		defer seed.Close()
		return EnrollAgent(ctx, stateDir, seed, protocol, release, time.Now())
	})
}

func serveAgent(ctx context.Context, stateDir, protocol, release, secretSocket string, enroll func() (AgentIdentity, error)) error {
	if !filepath.IsAbs(stateDir) {
		return errors.New("invalid guest service configuration")
	}
	identity, err := LoadAgentIdentity(stateDir, time.Now())
	if err != nil {
		identity, err = enroll()
		if err != nil {
			return err
		}
	}
	for {
		err := serveAgentIdentity(ctx, identity, stateDir, protocol, release, secretSocket)
		if !errors.Is(err, errAgentIdentityRenewed) {
			return err
		}
		identity, err = LoadAgentIdentity(stateDir, time.Now())
		if err != nil {
			return err
		}
	}
}

func serveAgentIdentity(ctx context.Context, identity AgentIdentity, stateDir, protocol, release, secretSocket string) error {
	client := identity.pollClient("")
	defer client.CloseIdleConnections()
	if secretSocket != "" {
		executable, err := os.Executable()
		if err != nil {
			return errors.New("cannot identify the running workspace command")
		}
		if err := prepareWorkspaceCommand(executable, filepath.Dir(secretSocket)); err != nil {
			return err
		}
		stopSecrets, err := listenSecretSocket(ctx, secretSocket, client, identity.PollEndpoint)
		if err != nil {
			return err
		}
		defer stopSecrets()
	}
	watches := newProcessWatches(time.Now)
	if secretSocket != "" {
		stopWatches, err := listenProcessWatchSocket(ctx, filepath.Join(filepath.Dir(secretSocket), "activity.sock"), watches)
		if err != nil {
			return err
		}
		defer stopWatches()
	}
	tunnel := &agentTunnel{protocol: protocol, tls: client.Transport.(*http.Transport).TLSClientConfig.Clone(),
		retry: 5 * time.Second, serve: ServeTunnel}
	defer tunnel.close()
	journal, err := loadLocalJournal(filepath.Join(stateDir, "operation.json"))
	if err != nil {
		return err
	}
	journal.downloadUpdate = func(ctx context.Context, productRelease string) ([sha256.Size]byte, error) {
		key, err := loadGuestUpdatePublisherKey(guestUpdatePublisherKeyPath)
		if err != nil {
			return [sha256.Size]byte{}, err
		}
		return downloadGuestUpdate(ctx, client, identity.PollEndpoint, stateDir, productRelease, protocol, key)
	}
	project := filepath.Join(stateDir, "project")
	runtime := devContainer{workspace: project, credentials: secretSocket != "", ide: true, retainHome: true,
		applyImageCache: func(mode string) error { return updateImageCache(stateDir, identity.PollEndpoint, mode) },
		repairImageFile: "/usr/local/share/shiftbox/repair-image", repairSocketDir: repairDirectory, repository: &repositoryPreparation{
			project: project,
			token: func(ctx context.Context) (string, error) {
				return guestGitAccessToken(ctx, client, identity.PollEndpoint)
			},
		}}
	probes := &probeReporter{socket: proxy.IDESocket}
	firstPoll := true
	renewAfter := guestRenewAfter(identity.Certificate.Leaf)
	for {
		now := time.Now()
		if !now.Before(renewAfter) {
			if _, err := RenewAgentIdentity(ctx, stateDir, now); err == nil {
				return errAgentIdentityRenewed
			}
			// Continue with the current identity when the controller has not
			// replaced it. If the response was lost, the next attempt reuses
			// the pending key even across a supervisor restart.
		}
		input := newGuestPollRequest(protocol, release, journal)
		input.WatchedProcesses = watches.sample(now)
		var delay time.Duration
		poll := func(ctx context.Context) error {
			var err error
			delay, err = pollAgent(ctx, client, identity.PollEndpoint, input, journal, runtime, tunnel, probes)
			return err
		}
		if firstPoll {
			executable, err := os.Executable()
			if err != nil {
				return errors.New("cannot identify the running guest agent")
			}
			if err := confirmRunningGuestTrial(ctx, stateDir, executable, poll); err != nil {
				return err
			}
			firstPoll = false
		} else if err := poll(ctx); err != nil {
			return err
		}
		if delay == 0 && ctx.Err() == nil {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
