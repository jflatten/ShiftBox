package ec2

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// TestSingleBootReconcilesLostResponse checks that recovery adopts the same running VM without power mutations.
func TestSingleBootReconcilesLostResponse(t *testing.T) {
	const imageID = "ami-11112222333344445"
	const publicData = `{"version":1,"launchReference":"public-reference"}`
	ref := provider.WorkspaceRef{InstallationID: "install", WorkspaceID: "workspace", Generation: 1, OwnershipMarker: "marker"}
	req := provider.CreateRequest{Ref: ref, OperationID: "operation", ArtifactID: "artifact", Release: "release",
		NetworkMode: NetworkMode, Resources: ReferenceResources(), BootstrapData: publicData}
	cfg := Config{Region: "us-east-2", AccountID: "123456789012", SubnetID: "subnet-test", SecurityGroupID: "sg-test", Deployment: "demo"}
	mock := &lostLaunchAPI{cfg: cfg}
	p := &Provider{cfg: cfg, client: mock, poll: time.Millisecond, now: time.Now,
		resolve: func(context.Context, string, string) (Image, error) {
			return Image{ID: imageID, RootDeviceName: "/dev/sda1", Architecture: "amd64", SingleBoot: true}, nil
		}}
	if _, err := p.Create(t.Context(), req); !errors.Is(err, provider.ErrOutcomeUnknown) {
		t.Fatalf("Create after lost response = %v, want uncertain result", err)
	}
	got, err := p.Create(t.Context(), req)
	if err != nil || got.Power != provider.Running || !got.DiskCapacityKnown || got.Effective != ReferenceResources() {
		t.Fatalf("reconciled Create = %+v, %v, want running reference instance", got, err)
	}
	if mock.launches != 1 || mock.starts != 0 || mock.stops != 0 || mock.token != launchToken(req) ||
		mock.userData != base64.StdEncoding.EncodeToString([]byte(publicData)) {
		t.Fatalf("AWS mutations = %d launches, %d starts, %d stops, want one launch with stable token and public user data", mock.launches, mock.starts, mock.stops)
	}
}
