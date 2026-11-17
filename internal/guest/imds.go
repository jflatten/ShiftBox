package guest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

const metadataEndpoint = "http://169.254.169.254"

// ServeEC2Agent reads enrollment directly from IMDSv2 when the retained guest
// identity needs enrollment. The raw payload never enters the guest filesystem.
func ServeEC2Agent(ctx context.Context, stateDir, protocol, release, secretSocket string) error {
	return serveAgent(ctx, stateDir, protocol, release, secretSocket, func() (AgentIdentity, error) {
		client := &http.Client{
			Transport: &http.Transport{DisableKeepAlives: true},
			Timeout:   5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		seed, err := readEC2EnrollmentSeed(ctx, client)
		if err != nil {
			return AgentIdentity{}, err
		}
		defer clear(seed)
		return EnrollAgent(ctx, stateDir, bytes.NewReader(seed), protocol, release, time.Now())
	})
}

func readEC2EnrollmentSeed(ctx context.Context, client *http.Client) ([]byte, error) {
	tokenReq, err := http.NewRequestWithContext(ctx, http.MethodPut, metadataEndpoint+"/latest/api/token", nil)
	if err != nil {
		return nil, err
	}
	tokenReq.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "60")
	token, err := readMetadata(client, tokenReq, 1024)
	if err != nil {
		return nil, err
	}
	defer clear(token)
	seedReq, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataEndpoint+"/latest/user-data", nil)
	if err != nil {
		return nil, err
	}
	seedReq.Header.Set("X-aws-ec2-metadata-token", string(token))
	return readMetadata(client, seedReq, 16<<10)
}

func readMetadata(client *http.Client, request *http.Request, limit int64) ([]byte, error) {
	unavailable := errors.New("EC2 enrollment metadata is unavailable")
	response, err := client.Do(request)
	if err != nil {
		return nil, unavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, unavailable
	}
	result, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(result) == 0 || int64(len(result)) > limit {
		clear(result)
		return nil, unavailable
	}
	return result, nil
}
