package guest

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

type rebuildSource struct {
	id      string
	home    string
	volumes []namedVolume
}

type namedVolume struct {
	Type, Source, Target string
}

func (d devContainer) rebuild(ctx context.Context) (containerConnection, error) {
	home, err := d.retainedHome()
	if err != nil || !d.retainHome || home == "" {
		return containerConnection{}, errors.New("rebuild requires a retained developer home")
	}
	out, err := d.runner()(ctx, "/usr/bin/docker", "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label="+developmentLabel)
	if err != nil {
		return containerConnection{}, err
	}
	id := strings.TrimSpace(string(out))
	if !dockerContainerID.MatchString(id) {
		return containerConnection{}, errors.New("rebuild requires exactly one development container")
	}
	volumes, err := d.namedVolumes(ctx, id)
	if err != nil {
		return containerConnection{}, err
	}
	previous := rebuildSource{id: id, home: home, volumes: volumes}
	if err := d.verifyRebuildMounts(ctx, id, previous); err != nil {
		return containerConnection{}, err
	}
	return d.configure(ctx, previous)
}

func (d devContainer) verifyReplacement(ctx context.Context, result devContainerResult, previous rebuildSource) error {
	if result.ID == previous.id {
		return errors.New("rebuild reused the existing development container")
	}
	if err := d.verifyRebuildMounts(ctx, result.ID, previous); err != nil {
		return err
	}
	return d.verifyHomeOwner(ctx, result, previous.home)
}

func (d devContainer) verifyRebuildMounts(ctx context.Context, id string, previous rebuildSource) error {
	mounts, err := containerMounts(ctx, id, d.runner())
	if err != nil {
		return err
	}
	mounted, err := retainedHomeMounted(mounts, d.retainedHomeData(), previous.home)
	if err != nil || !mounted {
		return errors.New("development container is missing the retained home mount")
	}
	if !slices.ContainsFunc(mounts, func(mount dockerMount) bool {
		return mount.Type == "bind" && mount.Source == d.workspace && mount.Destination != previous.home && mount.RW
	}) {
		return errors.New("development container is missing the retained checkout mount")
	}
	for _, volume := range previous.volumes {
		if !slices.ContainsFunc(mounts, func(mount dockerMount) bool {
			return mount.Type == "volume" && mount.Name == volume.Source && mount.Destination == volume.Target
		}) {
			return errors.New("development container is missing a retained application volume")
		}
	}
	return nil
}

// Docker assigns a generated name to anonymous volumes at runtime. Read the
// original mount declarations to identify only explicitly named volumes.
func (d devContainer) namedVolumes(ctx context.Context, id string) ([]namedVolume, error) {
	out, err := d.runner()(ctx, "/usr/bin/docker", "container", "inspect", "--format", "{{json .HostConfig.Mounts}}", "--", id)
	if err != nil {
		return nil, err
	}
	var mounts []namedVolume
	if err := json.Unmarshal(out, &mounts); err != nil {
		return nil, errors.New("invalid Dev Container mount declarations")
	}
	return slices.DeleteFunc(mounts, func(mount namedVolume) bool {
		return mount.Type != "volume" || mount.Source == ""
	}), nil
}
