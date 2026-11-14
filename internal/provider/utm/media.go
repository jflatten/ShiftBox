package utm

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jflatten/ShiftBox/internal/provider"
)

// ReplaceRecoveryMedia updates a stopped VM and verifies its drive inventory.
// UTM exposes the replacement's size. Guest consumption requires separate evidence.
func (p *PowerController) ReplaceRecoveryMedia(ctx context.Context, req provider.EnrollmentRequest, mediaID string) (provider.Inventory, error) {
	driveID, scoped := strings.CutPrefix(mediaID, req.Ref.InstanceID+"/")
	if !req.Eligible || req.OperationID == "" || req.Ref.InstanceID == "" || !filepath.IsAbs(req.MediaPath) ||
		!scoped || driveID == "" {
		return provider.Inventory{}, provider.ErrInvalidState
	}
	info, err := os.Lstat(req.MediaPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return provider.Inventory{}, provider.ErrInvalidState
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	before, err := p.inspect(ctx, req.Ref)
	if err != nil {
		return before, err
	}
	if before.Power != provider.Stopped || verifyMediaReplacement(before, before, mediaID) != nil {
		return before, provider.ErrInvalidState
	}
	if err := ctx.Err(); err != nil {
		return before, err
	}
	err = replaceMedia(ctx, p.version, req, driveID)
	after, inspectErr := p.inspect(ctx, req.Ref)
	if err != nil && !errors.Is(err, provider.ErrOutcomeUnknown) {
		return after, err
	}
	if err != nil || inspectErr != nil {
		return after, errors.Join(provider.ErrOutcomeUnknown, err, inspectErr)
	}
	if err := verifyMediaReplacement(before, after, mediaID); err != nil {
		return after, err
	}
	const mib = 1 << 20
	for _, resource := range after.Resources {
		if resource.ID == mediaID && resource.HostBytes != nil && *resource.HostBytes == info.Size()/mib*mib {
			return after, nil
		}
	}
	return after, provider.ErrOutcomeUnknown
}

func verifyMediaReplacement(before, after provider.Inventory, mediaID string) error {
	if before.InstanceID == "" || before.Ref != after.Ref || before.InstanceID != after.InstanceID ||
		before.Power != provider.Stopped || after.Power != provider.Stopped ||
		before.NetworkMode != after.NetworkMode || before.Effective != after.Effective ||
		before.ArtifactID != after.ArtifactID || before.Release != after.Release ||
		before.DiskCapacityKnown != after.DiskCapacityKnown ||
		len(before.Resources) != len(after.Resources) {
		return provider.ErrOutcomeUnknown
	}
	found := false
	for i, old := range before.Resources {
		current := after.Resources[i]
		if old.ID == mediaID && old.Type == "media" && !old.Immutable && !old.Shared {
			found = true
			current.HostBytes = old.HostBytes
			current.GuestBytes = old.GuestBytes
		}
		if !reflect.DeepEqual(old, current) {
			return provider.ErrOutcomeUnknown
		}
	}
	if !found {
		return provider.ErrOutcomeUnknown
	}
	return nil
}

const replaceMediaScript = `
use framework "Foundation"

on run argv
    set expectedVersion to item 1 of argv
    set vmID to item 2 of argv
    set driveID to item 3 of argv
    set medium to POSIX file (item 4 of argv) as alias
    tell application "/Applications/UTM.app"
        if UTM version is not expectedVersion then return "version"
        set vm to virtual machine id vmID
        if not (exists vm) then return "ownership"
        if backend of vm is not qemu then return "ownership"
        set cfg to configuration of vm
        if not my matchesOwner(notes of cfg, argv) then return "ownership"
        if status of vm is not stopped then return "state"
        -- Omitting an existing drive from the update removes it.
        set updatedDrives to {}
        set seenIDs to {}
        set found to false
        repeat with driveEntry in drives of cfg
            set existingID to id of driveEntry
            if seenIDs contains existingID then return "state"
            set end of seenIDs to existingID
            if existingID is driveID then
                if not removable of driveEntry then return "state"
                set end of updatedDrives to {id:existingID, source:medium}
                set found to true
            else
                set end of updatedDrives to {id:existingID}
            end if
        end repeat
        if not found then return "state"
        update configuration vm with {drives:updatedDrives}
    end tell
    return "issued"
end run

on matchesOwner(vmNotes, argv)
    considering case
        if vmNotes does not start with "shiftbox:" then return false
        set jsonText to current application's NSString's stringWithString:(text 10 thru -1 of vmNotes)
        set jsonData to jsonText's dataUsingEncoding:(current application's NSUTF8StringEncoding)
        set owner to current application's NSJSONSerialization's JSONObjectWithData:jsonData options:0 |error|:(missing value)
        if owner is missing value then return false
        return ((owner's objectForKey:"installationId") as text) is item 5 of argv ¬
            and ((owner's objectForKey:"workspaceId") as text) is item 6 of argv ¬
            and ((owner's objectForKey:"generation") as text) is item 7 of argv ¬
            and ((owner's objectForKey:"marker") as text) is item 8 of argv
    end considering
end matchesOwner
`

func replaceMedia(ctx context.Context, version string, req provider.EnrollmentRequest, driveID string) error {
	if version == "" {
		return provider.ErrCapabilityUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ref := req.Ref
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", replaceMediaScript, "--",
		version, ref.InstanceID, driveID, req.MediaPath, ref.InstallationID, ref.WorkspaceID,
		strconv.FormatInt(ref.Generation, 10), ref.OwnershipMarker)
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && bytes.Contains(exit.Stderr, []byte("(-1743)")) {
			return provider.ErrPermission
		}
		return provider.ErrOutcomeUnknown
	}
	switch strings.TrimSpace(string(out)) {
	case "issued":
		return nil
	case "version":
		return provider.ErrCapabilityUnsupported
	case "ownership":
		return provider.ErrOwnershipMismatch
	case "state":
		return provider.ErrInvalidState
	default:
		return provider.ErrOutcomeUnknown
	}
}
