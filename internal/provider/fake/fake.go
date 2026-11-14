// Package fake implements a deterministic, in-memory provider for controller tests.
package fake

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/jflatten/ShiftBox/internal/provider"
)

type key struct{ installation, workspace string }

type machine struct {
	inventory       provider.Inventory
	createOperation string
	enrolled        bool
}

// Provider models owned resources independently from controller intent.
type Provider struct {
	mu                         sync.Mutex
	machines                   map[key]*machine
	sequence                   int
	lostReply                  string
	partialDelete, unavailable bool
}

var _ provider.Provider = (*Provider)(nil)

func New() *Provider { return &Provider{machines: make(map[key]*machine)} }

func (p *Provider) LoseReply(method string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lostReply = method
}

func (p *Provider) FailDelete() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.partialDelete = true
}

func (p *Provider) SetUnavailable(value bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unavailable = value
}

func (p *Provider) available(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.unavailable {
		return provider.ErrUnavailable
	}
	return nil
}

func (p *Provider) Capabilities(ctx context.Context) (provider.Capabilities, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.available(ctx); err != nil {
		return provider.Capabilities{}, err
	}
	return provider.Capabilities{Name: "fake", Version: "1", Architecture: "amd64", ExecutionMode: "emulated", Evidence: "fake provider",
		Create: true, Start: true, Stop: true, Delete: true, NetworkModes: []string{"nat"}, RecoveryMechanisms: []string{"media"}, NetworkStoppedOnly: true}, nil
}

func workspaceKey(ref provider.WorkspaceRef) key { return key{ref.InstallationID, ref.WorkspaceID} }

func (p *Provider) lookup(ref provider.WorkspaceRef) (*machine, error) {
	m := p.machines[workspaceKey(ref)]
	if m == nil {
		return nil, provider.ErrNotFound
	}
	got := m.inventory.Ref
	if got.Generation != ref.Generation || got.OwnershipMarker != ref.OwnershipMarker ||
		(ref.InstanceID != "" && ref.InstanceID != m.inventory.InstanceID) {
		return nil, provider.ErrOwnershipMismatch
	}
	return m, nil
}

func observe(m *machine) provider.Inventory {
	got := m.inventory
	got.Resources = slices.Clone(got.Resources)
	got.ObservedAt = time.Now().UTC()
	return got
}

func (p *Provider) Inspect(ctx context.Context, ref provider.WorkspaceRef) (provider.Inventory, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.available(ctx); err != nil {
		return provider.Inventory{}, err
	}
	m, err := p.lookup(ref)
	if err != nil {
		return provider.Inventory{}, err
	}
	return observe(m), nil
}

func (p *Provider) reply(method string, m *machine) (provider.Inventory, error) {
	if p.lostReply == method {
		p.lostReply = ""
		return provider.Inventory{}, provider.ErrOutcomeUnknown
	}
	return observe(m), nil
}

func (p *Provider) Create(ctx context.Context, req provider.CreateRequest) (provider.Inventory, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.available(ctx); err != nil {
		return provider.Inventory{}, err
	}
	if req.Ref.InstallationID == "" || req.Ref.WorkspaceID == "" || req.Ref.OwnershipMarker == "" ||
		req.Ref.Generation < 1 || req.OperationID == "" || req.ArtifactID == "" {
		return provider.Inventory{}, provider.ErrInvalidState
	}
	if req.NetworkMode != "" && req.NetworkMode != "nat" {
		return provider.Inventory{}, provider.ErrCapabilityUnsupported
	}
	if m, err := p.lookup(req.Ref); err == nil {
		if m.createOperation != req.OperationID || m.inventory.Power == provider.Deleted {
			return provider.Inventory{}, provider.ErrInvalidState
		}
		return p.reply("create", m)
	} else if err != provider.ErrNotFound {
		return provider.Inventory{}, err
	}
	if req.Ref.InstanceID != "" {
		return provider.Inventory{}, provider.ErrOwnershipMismatch
	}
	p.sequence++
	id := fmt.Sprintf("fake-vm-%d", p.sequence)
	ref := req.Ref
	ref.InstanceID = id
	m := &machine{createOperation: req.OperationID, enrolled: req.EnrollmentMedia != "", inventory: provider.Inventory{
		Ref: ref, InstanceID: id, Power: provider.Stopped, ArtifactID: req.ArtifactID, Release: req.Release, NetworkMode: req.NetworkMode, Effective: req.Resources,
		Resources: []provider.OwnedResource{
			{ID: id, Type: "vm", OwnershipMarker: ref.OwnershipMarker, Generation: ref.Generation},
			{ID: id + "-disk", Type: "disk", OwnershipMarker: ref.OwnershipMarker, Generation: ref.Generation},
			{ID: req.ArtifactID, Type: "catalog", Immutable: true, Shared: true},
		},
	}}
	if req.EnrollmentMedia != "" {
		m.inventory.Resources = append(m.inventory.Resources, provider.OwnedResource{ID: req.EnrollmentMedia, Type: "media", OwnershipMarker: ref.OwnershipMarker, Generation: ref.Generation})
	}
	p.machines[workspaceKey(ref)] = m
	return p.reply("create", m)
}

func (p *Provider) mutate(ctx context.Context, ref provider.WorkspaceRef, operation, method string, change func(*machine) error) (provider.Inventory, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.available(ctx); err != nil {
		return provider.Inventory{}, err
	}
	if ref.InstanceID == "" || operation == "" {
		return provider.Inventory{}, provider.ErrInvalidState
	}
	m, err := p.lookup(ref)
	if err != nil {
		return provider.Inventory{}, err
	}
	if err := change(m); err != nil {
		return observe(m), err
	}
	return p.reply(method, m)
}

func (p *Provider) Start(ctx context.Context, req provider.StartRequest) (provider.Inventory, error) {
	return p.mutate(ctx, req.Ref, req.OperationID, "start", func(m *machine) error {
		if req.NetworkMode != "" && req.NetworkMode != m.inventory.NetworkMode {
			return provider.ErrInvalidState
		}
		if m.inventory.Power == provider.Running {
			return nil
		}
		if m.inventory.Power != provider.Stopped || (req.RequiresEnrollment && !m.enrolled) {
			return provider.ErrInvalidState
		}
		m.inventory.Power = provider.Running
		m.enrolled = false
		return nil
	})
}

func (p *Provider) Stop(ctx context.Context, req provider.StopRequest) (provider.Inventory, error) {
	return p.mutate(ctx, req.Ref, req.OperationID, "stop", func(m *machine) error {
		if m.inventory.Power != provider.Running && m.inventory.Power != provider.Stopped {
			return provider.ErrInvalidState
		}
		m.inventory.Power = provider.Stopped
		return nil
	})
}

func (p *Provider) Delete(ctx context.Context, req provider.DeleteRequest) (provider.Inventory, error) {
	return p.mutate(ctx, req.Ref, req.OperationID, "delete", func(m *machine) error {
		if m.inventory.Power == provider.Deleted {
			return nil
		}
		if m.inventory.Power != provider.Stopped {
			return provider.ErrInvalidState
		}
		if p.partialDelete {
			p.partialDelete = false
			for i, r := range m.inventory.Resources {
				if !r.Shared && !r.Immutable {
					m.inventory.Resources = slices.Delete(m.inventory.Resources, i, i+1)
					return provider.ErrPartial
				}
			}
		}
		m.inventory.Resources = slices.DeleteFunc(m.inventory.Resources, func(r provider.OwnedResource) bool { return !r.Shared && !r.Immutable })
		m.inventory.Power = provider.Deleted
		return nil
	})
}

func (p *Provider) ApplyNetwork(ctx context.Context, req provider.NetworkRequest) (provider.Inventory, error) {
	return p.mutate(ctx, req.Ref, req.OperationID, "network", func(m *machine) error {
		if req.Mode != "nat" {
			return provider.ErrCapabilityUnsupported
		}
		if m.inventory.Power != provider.Stopped {
			return provider.ErrInvalidState
		}
		m.inventory.NetworkMode = req.Mode
		return nil
	})
}

func (p *Provider) DeliverEnrollment(ctx context.Context, req provider.EnrollmentRequest) (provider.DeliveryResult, error) {
	inv, err := p.mutate(ctx, req.Ref, req.OperationID, "enrollment", func(m *machine) error {
		if !req.Eligible || req.MediaPath == "" || m.inventory.Power != provider.Stopped {
			return provider.ErrInvalidState
		}
		for i, r := range m.inventory.Resources {
			if r.Type == "media" && !r.Shared && !r.Immutable {
				m.inventory.Resources[i].ID = req.MediaPath
				m.enrolled = true
				return nil
			}
		}
		return provider.ErrInvalidState
	})
	if err != nil {
		return provider.DeliveryResult{Inventory: inv}, err
	}
	return provider.DeliveryResult{Inventory: inv, MediaID: req.MediaPath}, nil
}
