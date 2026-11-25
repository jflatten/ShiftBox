import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MantineProvider } from '@mantine/core';
import { Providers } from '../../app/providers';
import { Infrastructure } from './infrastructure';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

const session = {
  user: { id: 'operator', role: 'administrator' }, mode: 'normal', installationState: 'active',
  permissions: ['infrastructure.view', 'infrastructure.manage'], csrfToken: 'fixture',
};

const status = {
  provider: { name: 'utm', version: '5.0.8', architecture: 'amd64', networkModes: ['shared'] },
  capacity: { cpuCount: 4, memoryMiB: 8192, diskMiB: 131072 },
  version: 2,
  conditions: [
    { key: 'browser_certificate.expiring', severity: 'warning', active: true, detail: 'Replace the browser certificate before December 14.' },
    { key: 'storage.pressure', severity: 'critical', active: true, detail: 'Free space on the workspace storage volume before allocating another workspace.' },
    { key: 'storage.recovered', severity: 'info', active: false, detail: 'A previous storage warning cleared.' },
  ],
};

function json(payload: unknown) {
  return { ok: true, status: 200, json: async () => payload } as Response;
}

// Administrators find resource profiles immediately after provider details,
// with separate cards for capacity limits and inactivity policy.
test('orders the resource cards and gives each settings form its own section', async () => {
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json({ ...status,
      keepAlivePolicy: { maximumMinutes: 0, allowedMinutes: [30, 60, 120, 240, 480, 720, 0], defaultMinutes: 30, version: 1 } });
    if (input === '/api/v1/admin/resource-profiles') return json([]);
    throw new Error('unexpected request');
  });
  render(<Providers><Infrastructure /></Providers>);
  const provider = await screen.findByRole('region', { name: 'Provider' });
  const profiles = screen.getByRole('region', { name: 'Resource profiles' });
  const capacity = await screen.findByRole('region', { name: 'Capacity budget' });
  expect(provider.nextElementSibling).toBe(profiles);
  expect(profiles.nextElementSibling).toBe(capacity);
  const shutdown = screen.getByRole('region', { name: 'Inactivity shutdown' });
  expect(capacity.nextElementSibling).toBe(shutdown);
  expect(within(capacity).getByRole('button', { name: 'Save capacity budget' })).toBeVisible();
  expect(within(shutdown).getByRole('textbox', { name: 'Maximum inactivity timeout' })).toHaveValue('Unlimited');
  expect(within(shutdown).getByRole('combobox', { name: 'Default keep-alive' })).toHaveValue('30 minutes');
  expect(within(shutdown).getByRole('button', { name: 'Save keep-alive settings' })).toBeVisible();
});

// Clearing individual limits must persist Unlimited without changing finite
// dimensions. Refreshed unlimited values stay blank and discard restores them.
test('saves independent unlimited limits and restores saved blanks when discarding', async () => {
  let current = { ...status, capacity: { ...status.capacity } };
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json(current);
    if (input === '/api/v1/admin/resource-profiles') return json([]);
    if (input === '/api/v1/admin/capacity' && init?.method === 'PUT') {
      current = { ...current, capacity: JSON.parse(String(init.body)), version: current.version + 1 };
      return json(null);
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Infrastructure /></Providers>);
  await user.clear(await screen.findByRole('textbox', { name: 'CPU limit' }));
  await user.click(screen.getByRole('button', { name: 'Save capacity budget' }));
  expect(await screen.findByText('Capacity budget saved.')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/capacity', expect.objectContaining({
    body: JSON.stringify({ cpuCount: 0, memoryMiB: 8192, diskMiB: 131072 }),
  }));
  expect(screen.getByRole('textbox', { name: 'CPU limit' })).toHaveValue('');
  expect(screen.getByRole('textbox', { name: 'CPU limit' })).toHaveAttribute('placeholder', 'Unlimited');
  expect(screen.getByRole('textbox', { name: 'CPU limit' })).not.toBeRequired();
  expect(screen.getByRole('button', { name: 'Save capacity budget' })).toBeDisabled();
  await user.clear(screen.getByRole('textbox', { name: 'Memory limit (MiB)' }));
  await user.clear(screen.getByRole('textbox', { name: 'Disk limit (MiB)' }));
  await user.click(screen.getByRole('button', { name: 'Save capacity budget' }));
  expect(await screen.findByText('Capacity budget saved.')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/capacity', expect.objectContaining({
    body: JSON.stringify({ cpuCount: 0, memoryMiB: 0, diskMiB: 0 }),
  }));
  expect(screen.getAllByPlaceholderText('Unlimited')).toHaveLength(3);
  await user.type(screen.getByRole('textbox', { name: 'CPU limit' }), '8');
  await user.click(screen.getByRole('button', { name: 'Discard changes' }));
  expect(screen.getByRole('textbox', { name: 'CPU limit' })).toHaveValue('');
  expect(screen.getByRole('button', { name: 'Save capacity budget' })).toBeDisabled();
  await user.type(screen.getByRole('textbox', { name: 'CPU limit' }), '8');
  await user.click(screen.getByRole('button', { name: 'Save capacity budget' }));
  expect(await screen.findByText('Capacity budget saved.')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/capacity', expect.objectContaining({
    body: JSON.stringify({ cpuCount: 8, memoryMiB: 0, diskMiB: 0 }),
  }));
  expect(screen.getByRole('textbox', { name: 'CPU limit' })).toHaveValue('8');
});

// Zero is the API representation of Unlimited; entered limits must still
// be positive whole numbers so an invalid draft never sends a mutation.
test('rejects an invalid numeric capacity draft before sending it', async () => {
  const fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json(status);
    if (input === '/api/v1/admin/resource-profiles') return json([]);
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Infrastructure /></Providers>);
  const cpu = await screen.findByRole('textbox', { name: 'CPU limit' });
  await user.clear(cpu);
  await user.type(cpu, '0');
  await user.click(screen.getByRole('button', { name: 'Save capacity budget' }));
  expect(await screen.findByText('Enter a positive whole number.')).toBeVisible();
  expect(cpu).toHaveFocus();
  expect(fetch.mock.calls.every(([input]) => input !== '/api/v1/admin/capacity')).toBe(true);
});

// Administrators without write permission must see unlimited budget labels
// rather than zero resource amounts, including alongside a finite limit.
test('shows unlimited limits in a read-only capacity summary', async () => {
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json({ ...session, permissions: ['infrastructure.view'] });
    if (input === '/api/v1/admin/infrastructure') return json({ ...status, capacity: { cpuCount: 0, memoryMiB: 8192, diskMiB: 0 } });
    if (input === '/api/v1/admin/resource-profiles') return json([]);
    throw new Error('unexpected request');
  });
  render(<Providers><Infrastructure /></Providers>);
  expect(await screen.findByText('Unlimited CPU · 8192 MiB memory · Unlimited disk')).toBeVisible();
});

// A fresh UTM installation can create a profile and see it in the table immediately.
test('lists a resource profile after creation', async () => {
  const profiles: unknown[] = [];
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json(status);
    if (input === '/api/v1/admin/resource-profiles') {
      if (init?.method === 'POST') {
        profiles.push({ ...JSON.parse(String(init.body)), id: 'profile', enabled: true, version: 1 });
        return json({});
      }
      return json(profiles);
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Infrastructure /></Providers>);
  expect(await screen.findByText('No resource profiles yet.')).toBeVisible();
  await user.click(screen.getByRole('button', { name: 'New resource profile' }));
  const dialog = await screen.findByRole('dialog', { name: 'New resource profile' });
  await user.type(within(dialog).getByLabelText(/Profile name/), 'AWS demo');
  await user.type(within(dialog).getByLabelText(/vCPUs/), '2');
  await user.type(within(dialog).getByLabelText(/Memory \(MiB\)/), '4096');
  await user.type(within(dialog).getByLabelText(/Disk \(MiB\)/), '32768');
  await user.click(within(dialog).getByRole('button', { name: 'Create resource profile' }));
  expect(await screen.findByText('Resource profile created.')).toBeVisible();
  expect(within(screen.getByRole('table', { name: 'Resource profiles' })).getByRole('rowheader', { name: 'AWS demo' })).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/resource-profiles', expect.objectContaining({
    method: 'POST', body: JSON.stringify({ name: 'AWS demo', cpuCount: 2, memoryMiB: 4096, diskMiB: 32768 }),
  }));
});


// EC2 uses the authoritative instance shape and submits the separately entered disk in MiB.
test('creates an EC2 resource profile with an instance type and separate EBS size', async () => {
  const collection = '/api/v1/admin/resource-profiles';
  const instanceTypes = [
    { name: 't3.medium', architecture: 'amd64', cpuCount: 2, memoryMiB: 4096 },
    { name: 'm6i.xlarge', architecture: 'amd64', cpuCount: 4, memoryMiB: 16384 },
  ];
  let profiles: unknown[] = [];
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json({
      ...status, provider: { ...status.provider, name: 'ec2', instanceTypes, minimumDiskGiB: 32, maximumDiskGiB: 2047 },
    });
    if (input === '/api/v1/templates/catalog') return json({ platformImages: [
      { id: 'amd-image', name: 'Ubuntu AMD64', providerKind: 'ec2', architecture: 'amd64', amiId: 'ami-amd' },
    ] });
    if (input === collection && init?.method === 'POST') {
      profiles = [{ ...JSON.parse(String(init.body)), cpuCount: 4, memoryMiB: 16384, id: 'profile', enabled: true, version: 1 }];
      return json({});
    }
    if (input === collection) return json(profiles);
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><MantineProvider env="test"><Infrastructure /></MantineProvider></Providers>);
  await screen.findByText('AWS EC2');
  await user.click(screen.getByRole('button', { name: 'New resource profile' }));
  const dialog = within(await screen.findByRole('dialog', { name: 'New resource profile' }));
  await user.type(dialog.getByRole('textbox', { name: 'Profile name' }), 'AWS compute');
  expect(dialog.getByRole('combobox', { name: 'EC2 instance type' })).toBeDisabled();
  await user.click(await dialog.findByRole('combobox', { name: 'AMI image' }));
  await user.click(await screen.findByRole('option', { name: 'Ubuntu AMD64' }));
  await user.click(dialog.getByRole('combobox', { name: 'EC2 instance type' }));
  await user.click(await screen.findByRole('option', { name: 'm6i.xlarge' }));
  expect(dialog.getByRole('textbox', { name: 'vCPUs' })).toHaveValue('4');
  expect(dialog.getByRole('textbox', { name: 'Memory (MiB)' })).toHaveValue('16,384');
  expect(dialog.getByRole('textbox', { name: 'vCPUs' })).toHaveAttribute('readonly');
  const disk = dialog.getByRole('textbox', { name: 'EBS disk (GiB)' });
  await user.clear(disk);
  await user.type(disk, '64');
  await user.click(dialog.getByRole('button', { name: 'Create resource profile' }));
  expect(await screen.findByText('Resource profile created.')).toBeVisible();
  expect(screen.getByRole('row', { name: /AWS compute.*m6i.xlarge/ })).toBeVisible();
  expect(fetch).toHaveBeenCalledWith(collection, expect.objectContaining({
    method: 'POST', body: JSON.stringify({ name: 'AWS compute', platformArtifactId: 'amd-image', instanceType: 'm6i.xlarge', diskMiB: 65536 }),
  }));
});

// Switching AMI architecture clears an incompatible type and compute while retaining the chosen EBS size.
test('filters EC2 types by AMI and clears an incompatible selection', async () => {
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json({ ...status, provider: { name: 'ec2',
      instanceTypes: [
        { name: 't3.medium', architecture: 'amd64', cpuCount: 2, memoryMiB: 4096 },
        { name: 'm7g.large', architecture: 'arm64', cpuCount: 2, memoryMiB: 8192 },
      ], minimumDiskGiB: 32, maximumDiskGiB: 2047 } });
    if (input === '/api/v1/admin/resource-profiles') return json([]);
    if (input === '/api/v1/templates/catalog') return json({ platformImages: [
      { id: 'amd-image', name: 'Ubuntu AMD64', providerKind: 'ec2', architecture: 'amd64' },
      { id: 'arm-image', name: 'Ubuntu ARM64', providerKind: 'ec2', architecture: 'arm64' },
    ] });
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><MantineProvider env="test"><Infrastructure /></MantineProvider></Providers>);
  await screen.findByText('AWS EC2');
  await user.click(screen.getByRole('button', { name: 'New resource profile' }));
  const dialog = within(screen.getByRole('dialog', { name: 'New resource profile' }));
  const image = await dialog.findByRole('combobox', { name: 'AMI image' });
  await user.click(image);
  await user.click(await screen.findByRole('option', { name: 'Ubuntu AMD64' }));
  await user.click(dialog.getByRole('combobox', { name: 'EC2 instance type' }));
  expect(screen.queryByRole('option', { name: 'm7g.large' })).not.toBeInTheDocument();
  await user.click(screen.getByRole('option', { name: 't3.medium' }));
  expect(dialog.getByRole('textbox', { name: 'vCPUs' })).toHaveValue('2');
  const disk = dialog.getByRole('textbox', { name: 'EBS disk (GiB)' });
  await user.clear(disk);
  await user.type(disk, '64');
  await user.click(dialog.getByRole('combobox', { name: 'AMI image' }));
  await user.click(await screen.findByRole('option', { name: 'Ubuntu ARM64' }));
  expect(dialog.getByRole('combobox', { name: 'EC2 instance type' })).toHaveValue('');
  expect(dialog.getByRole('textbox', { name: 'vCPUs' })).toHaveValue('');
  expect(dialog.getByRole('textbox', { name: 'Memory (MiB)' })).toHaveValue('');
  expect(disk).toHaveValue('64');
  await user.click(dialog.getByRole('combobox', { name: 'EC2 instance type' }));
  expect(screen.queryByRole('option', { name: 't3.medium' })).not.toBeInTheDocument();
  await user.click(screen.getByRole('option', { name: 'm7g.large' }));
  expect(dialog.getByRole('textbox', { name: 'Memory (MiB)' })).toHaveValue('8,192');
});

// An unavailable AMI catalog blocks creation until an explicit retry succeeds; an empty catalog explains the next step.
test('recovers the AMI catalog and explains missing images', async () => {
  let fail = true;
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json({ ...status, provider: { name: 'ec2',
      instanceTypes: [{ name: 't3.medium', architecture: 'amd64', cpuCount: 2, memoryMiB: 4096 }],
      minimumDiskGiB: 32, maximumDiskGiB: 2047 } });
    if (input === '/api/v1/admin/resource-profiles') return json([]);
    if (input === '/api/v1/templates/catalog') {
      if (fail) throw new Error('Offline');
      return json({ platformImages: [] });
    }
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><MantineProvider env="test"><Infrastructure /></MantineProvider></Providers>);
  await screen.findByText('AWS EC2');
  await user.click(screen.getByRole('button', { name: 'New resource profile' }));
  const dialog = within(screen.getByRole('dialog', { name: 'New resource profile' }));
  const retry = await dialog.findByRole('button', { name: 'Retry loading AMI images' });
  expect(dialog.getByRole('button', { name: 'Create resource profile' })).toBeDisabled();
  fail = false;
  await user.click(retry);
  expect(await dialog.findByText('No approved AMI images are available. Import and validate a platform image first.')).toBeVisible();
  expect(dialog.getByRole('combobox', { name: 'EC2 instance type' })).toBeDisabled();
});

// Editing restores the saved AMI and type and refuses a disk smaller than the selected image's root volume.
test('edits the saved AMI binding and validates the image root disk minimum', async () => {
  const profile = { id: 'profile', name: 'AWS Small', platformArtifactId: 'amd-image', platformImageName: 'Ubuntu AMD64',
    instanceType: 't3.medium', cpuCount: 2, memoryMiB: 4096, diskMiB: 32768, enabled: true, version: 3 };
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json({ ...status, provider: { name: 'ec2',
      instanceTypes: [{ name: 't3.medium', architecture: 'amd64', cpuCount: 2, memoryMiB: 4096 }],
      minimumDiskGiB: 32, maximumDiskGiB: 2047 } });
    if (input === '/api/v1/admin/resource-profiles') return json([profile]);
    if (input === '/api/v1/templates/catalog') return json({ platformImages: [
      { id: 'amd-image', name: 'Ubuntu AMD64', providerKind: 'ec2', architecture: 'amd64', minimumDiskGiB: 32 },
      { id: 'large-image', name: 'Ubuntu large root', providerKind: 'ec2', architecture: 'amd64', minimumDiskGiB: 64 },
    ] });
    if (input === '/api/v1/admin/resource-profiles/profile' && init?.method === 'PUT') return json({});
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><MantineProvider env="test"><Infrastructure /></MantineProvider></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Actions for AWS Small' }));
  expect(within(screen.getByRole('table', { name: 'Resource profiles' })).getByText('Ubuntu AMD64')).toBeVisible();
  await user.click(await screen.findByRole('menuitem', { name: 'Edit' }));
  const dialog = within(screen.getByRole('dialog', { name: 'Edit resource profile' }));
  await waitFor(() => expect(dialog.getByRole('combobox', { name: 'AMI image' })).toHaveValue('Ubuntu AMD64'));
  expect(dialog.getByRole('combobox', { name: 'EC2 instance type' })).toHaveValue('t3.medium');
  expect(dialog.getByRole('textbox', { name: 'vCPUs' })).toHaveValue('2');
  await user.click(dialog.getByRole('combobox', { name: 'AMI image' }));
  await user.click(await screen.findByRole('option', { name: 'Ubuntu large root' }));
  expect(dialog.getByRole('combobox', { name: 'EC2 instance type' })).toHaveValue('t3.medium');
  await user.click(dialog.getByRole('button', { name: 'Save changes' }));
  expect(await dialog.findByText('Enter a whole number from 64 to 2,047 GiB.')).toBeVisible();
  expect(fetch.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(false);
  const disk = dialog.getByRole('textbox', { name: 'EBS disk (GiB)' });
  await user.clear(disk);
  await user.type(disk, '64');
  await user.click(dialog.getByRole('button', { name: 'Save changes' }));
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v1/admin/resource-profiles/profile', expect.objectContaining({
    method: 'PUT', headers: expect.objectContaining({ 'If-Match': '"3"' }),
    body: JSON.stringify({ name: 'AWS Small', platformArtifactId: 'large-image', instanceType: 't3.medium', diskMiB: 65536, enabled: true }),
  })));
});

// Publishing an AMI sends the manifest and selected family to validation while
// leaving the installation default alone.
test('publishes a Packer image to an AMI family', async () => {
  const manifest = { builds: [{ artifact_id: 'us-east-2:ami-test' }] };
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json({ ...status, provider: { name: 'ec2' } });
    if (input === '/api/v1/admin/resource-profiles') return json([]);
    if (input === '/api/v1/templates/catalog') return json({ platformImages: [] });
    if (input === '/api/v1/admin/platform-artifacts' && init?.method === 'POST') return json({ id: 'publish-operation' });
    if (input === '/api/v1/operations/publish-operation') return json({ id: 'publish-operation', status: 'succeeded', steps: [] });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  const page = () => <Providers><MantineProvider env="test"><Infrastructure /></MantineProvider></Providers>;
  const mounted = render(page());
  const panel = within(await screen.findByRole('region', { name: 'AMI families' }));
  await user.type(panel.getByRole('textbox', { name: 'AMI family' }), 'ubuntu-noble');
  await user.click(panel.getByRole('textbox', { name: 'Packer manifest' }));
  await user.paste(JSON.stringify(manifest));
  await user.click(panel.getByRole('button', { name: 'Validate and publish' }));
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v1/admin/platform-artifacts', expect.objectContaining({
    method: 'POST', headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture' }), body: JSON.stringify({ amiFamily: 'ubuntu-noble', manifest }),
  })));
  expect(await panel.findByText('Completed')).toBeVisible();
  mounted.unmount();
  const reloaded = render(page());
  expect(await within(await screen.findByRole('region', { name: 'AMI families' })).findByText('Completed')).toBeVisible();
  reloaded.unmount();
  window.sessionStorage.clear();
});

// Editing a family profile restores the family choice and saves the name of the
// family even after its newest validated image has changed.
test('keeps a resource profile bound to its AMI family after a patch', async () => {
  const profile = { id: 'family-profile', name: 'AWS Ubuntu', amiFamily: 'ubuntu-noble',
    instanceType: 't3.medium', cpuCount: 2, memoryMiB: 4096, diskMiB: 32768, enabled: true, version: 1 };
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json({ ...status, provider: { name: 'ec2',
      instanceTypes: [{ name: 't3.medium', architecture: 'amd64', cpuCount: 2, memoryMiB: 4096 }],
      minimumDiskGiB: 32, maximumDiskGiB: 2047 } });
    if (input === '/api/v1/admin/resource-profiles') return json([profile]);
    if (input === '/api/v1/templates/catalog') return json({ platformImages: [
      { id: 'patch-1', name: 'Ubuntu first patch', providerKind: 'ec2', architecture: 'amd64', minimumDiskGiB: 32, amiFamily: 'ubuntu-noble' },
      { id: 'patch-2', name: 'Ubuntu second patch', providerKind: 'ec2', architecture: 'amd64', minimumDiskGiB: 32, amiFamily: 'ubuntu-noble' },
    ] });
    if (input === '/api/v1/admin/resource-profiles/family-profile' && init?.method === 'PUT') return json({});
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><MantineProvider env="test"><Infrastructure /></MantineProvider></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Actions for AWS Ubuntu' }));
  expect(within(screen.getByRole('table', { name: 'Resource profiles' })).getByText('ubuntu-noble')).toBeVisible();
  await user.click(await screen.findByRole('menuitem', { name: 'Edit' }));
  const dialog = within(screen.getByRole('dialog', { name: 'Edit resource profile' }));
  await waitFor(() => expect(dialog.getByRole('combobox', { name: 'AMI image' })).toHaveValue('ubuntu-noble (latest)'));
  await user.click(dialog.getByRole('button', { name: 'Save changes' }));
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v1/admin/resource-profiles/family-profile', expect.objectContaining({
    method: 'PUT', body: JSON.stringify({ name: 'AWS Ubuntu', amiFamily: 'ubuntu-noble', instanceType: 't3.medium', diskMiB: 32768, enabled: true }),
  })));
});

// Editing refreshes the displayed name and state. A referenced profile remains
// available to administer after a rejected delete.
test('edits a profile and explains why deletion is blocked', async () => {
  const collection = '/api/v1/admin/resource-profiles';
  let profile = { id: 'profile', name: 'Small', cpuCount: 2, memoryMiB: 1024, diskMiB: 40, enabled: true, version: 1 };
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json(status);
    if (input === collection) return json([profile]);
    if (input === collection + '/profile' && init?.method === 'PUT') {
      profile = { ...profile, ...JSON.parse(String(init.body)), version: 2 };
      return json(null);
    }
    if (input === collection + '/profile' && init?.method === 'DELETE') {
      return { ok: false, status: 409, headers: new Headers({ 'Content-Type': 'application/problem+json' }),
        json: async () => ({ type: 'about:blank', status: 409, requestId: 'ref',
          code: 'infrastructure.resource_profile_in_use', title: 'A project revision uses this resource profile' }),
      } as Response;
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><MantineProvider env="test"><Infrastructure /></MantineProvider></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Actions for Small' }));
  await user.click(await screen.findByRole('menuitem', { name: 'Edit' }));
  const edit = await screen.findByRole('dialog', { name: 'Edit resource profile' });
  const name = within(edit).getByLabelText(/Profile name/);
  expect(name).toHaveValue('Small');
  await user.clear(name);
  await user.type(name, 'Medium');
  await user.click(within(edit).getByRole('switch', { name: 'Enabled' }));
  await user.click(within(edit).getByRole('button', { name: 'Save changes' }));
  expect(await screen.findByRole('rowheader', { name: 'Medium' })).toBeVisible();
  expect(screen.getByText('Disabled')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith(collection + '/profile', expect.objectContaining({
    method: 'PUT', headers: expect.objectContaining({ 'If-Match': '"1"', 'X-CSRF-Token': 'fixture' }),
  }));
  await user.click(screen.getByRole('button', { name: 'Actions for Medium' }));
  await user.click(await screen.findByRole('menuitem', { name: 'Delete' }));
  const remove = await screen.findByRole('dialog', { name: 'Delete resource profile' });
  await waitFor(() => expect(within(remove).getByRole('button', { name: 'Cancel' })).toHaveFocus());
  await user.click(within(remove).getByRole('button', { name: 'Delete resource profile' }));
  expect(await within(remove).findByText('A project revision uses this resource profile')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith(collection + '/profile', expect.objectContaining({
    method: 'DELETE', headers: expect.objectContaining({ 'If-Match': '"2"', 'X-CSRF-Token': 'fixture' }),
  }));
  await user.click(within(remove).getByRole('button', { name: 'Cancel' }));
  expect(screen.getByRole('rowheader', { name: 'Medium' })).toBeVisible();
});

// Maintenance stays available after the certificate and secrets tabs move.
test('leaves maintenance in infrastructure after settings move', async () => {
  const duplicateKeys = jest.spyOn(console, 'error').mockImplementation(() => {});
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json({ ...session, permissions: [...session.permissions, 'maintenance.manage'] });
    if (input === '/api/v1/admin/infrastructure') return json(status);
    if (input === '/api/v1/admin/resource-profiles') return json([]);
    if (input === '/api/v1/admin/maintenance') return json([]);
    if (input === '/api/v1/workspaces') return json({ items: [] });
    throw new Error('unexpected request');
  });
  try {
    const user = userEvent.setup();
    render(<Providers><Infrastructure /></Providers>);
    expect(await screen.findByRole('heading', { name: 'Provider' })).toBeVisible();
    expect(screen.getByRole('tab', { name: 'Platform' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.queryByRole('button', { name: 'Replace certificate' })).toBeNull();
    expect(screen.queryByRole('tab', { name: 'Certificates' })).toBeNull();
    expect(screen.queryByRole('tab', { name: 'Secrets' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Schedule maintenance' })).toBeNull();
    await user.click(screen.getByRole('tab', { name: 'Maintenance' }));
    expect(screen.getAllByRole('button', { name: 'Schedule maintenance' })).toHaveLength(1);
    expect(duplicateKeys.mock.calls.flat().join(' ')).not.toContain('same key');
  } finally {
    duplicateKeys.mockRestore();
  }
});

// A stale budget write reloads the current version without resending the
// mutation. Active certificate and storage warnings remain visible during the edit.
test('shows infrastructure warnings and refreshes after a concurrent capacity edit', async () => {
  let capacityChanged = false;
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json(capacityChanged
      ? { ...status, capacity: { ...status.capacity, cpuCount: 6 }, version: 3 } : status);
    if (input === '/api/v1/admin/resource-profiles') return json([]);
    if (input === '/api/v1/admin/capacity' && init?.method === 'PUT') {
      capacityChanged = true;
      return {
        ok: false, status: 412, headers: new Headers({ 'Content-Type': 'application/problem+json' }),
        json: async () => ({ type: 'about:blank', title: 'Refresh the capacity budget before updating',
          code: 'request.version_mismatch', status: 412, requestId: 'ref' }),
      } as Response;
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Infrastructure /></Providers>);
  const conditions = screen.getByRole('region', { name: 'Persistent conditions' });
  expect(await within(conditions).findByText('Replace the browser certificate before December 14.')).toBeVisible();
  expect(screen.getByText('UTM')).toBeVisible();
  expect(screen.getByText('5.0.8')).toBeVisible();
  expect(screen.getByText('x86-64 (amd64)')).toBeVisible();
  const cpu = screen.getByRole('textbox', { name: 'CPU limit' });
  await user.clear(cpu);
  await user.type(cpu, '5');
  await user.click(screen.getByRole('button', { name: 'Save capacity budget' }));
  expect(await screen.findByText('Refresh the capacity budget before updating')).toBeVisible();
  expect(await screen.findByDisplayValue('6')).toBeVisible();
  expect(within(conditions).getByText('Replace the browser certificate before December 14.')).toBeVisible();
  expect(within(conditions).getByText('Free space on the workspace storage volume before allocating another workspace.')).toBeVisible();
  expect(within(conditions).queryByText('A previous storage warning cleared.')).not.toBeInTheDocument();
  expect(fetch.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1);
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/capacity', expect.objectContaining({
    method: 'PUT', headers: expect.objectContaining({ 'If-Match': '"2"', 'X-CSRF-Token': 'fixture' }),
    body: JSON.stringify({ cpuCount: 5, memoryMiB: 8192, diskMiB: 131072 }),
  }));
});

// Direct visits by a member do not trigger a read of administrator data.
test('gates infrastructure data on the current administrator session', async () => {
  const fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json({ ...session, user: { id: 'member', role: 'member' }, permissions: [] });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  render(<Providers><Infrastructure /></Providers>);
  expect(await screen.findByText('Administrator access required.')).toBeVisible();
  expect(fetch.mock.calls.map(([input]) => input)).toEqual(['/api/v1/session']);
});

// Administrators with infrastructure read access can see warnings without upload controls.
test('requires infrastructure management permission to replace a certificate', async () => {
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json({ ...session, permissions: ['infrastructure.view'] });
    if (input === '/api/v1/admin/infrastructure') return json(status);
    if (input === '/api/v1/admin/resource-profiles') {
      return json([{ id: 'one', name: 'Small', cpuCount: 1, memoryMiB: 512, diskMiB: 40, enabled: true, version: 1 }]);
    }
    throw new Error('unexpected request');
  });
  render(<Providers><Infrastructure /></Providers>);
  expect(await screen.findByText('Browser certificate expires soon')).toBeVisible();
  expect(await screen.findByRole('rowheader', { name: 'Small' })).toBeVisible();
  expect(screen.queryByRole('button', { name: 'New resource profile' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Actions for Small' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Replace certificate' })).toBeNull();
  expect(screen.queryByRole('tab', { name: 'Certificates' })).toBeNull();
  expect(screen.queryByRole('tab', { name: 'Maintenance' })).toBeNull();
  expect(screen.queryByRole('tab', { name: 'Secrets' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Save capacity budget' })).toBeNull();
});

// Switching settings categories must not silently discard a capacity draft;
// Discard changes restores the saved values without issuing a mutation.
test('preserves a capacity draft across tabs and discards it explicitly', async () => {
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json(status);
    if (input === '/api/v1/admin/resource-profiles') return json([]);
    if (input === '/api/v1/admin/image-cache') return new Response(null, { status: 503 });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Infrastructure /></Providers>);
  const save = await screen.findByRole('button', { name: 'Save capacity budget' });
  expect(save).toBeDisabled();
  const cpu = screen.getByRole('textbox', { name: 'CPU limit' });
  await user.clear(cpu);
  await user.type(cpu, '5');
  expect(screen.getByText('You have unsaved capacity changes.')).toBeVisible();
  await user.click(screen.getByRole('tab', { name: 'Cache' }));
  expect(screen.getByText('Browser certificate expires soon')).toBeVisible();
  await user.click(screen.getByRole('tab', { name: 'Platform' }));
  expect(cpu).toHaveValue('5');
  expect(save).toBeEnabled();
  await user.click(screen.getByRole('button', { name: 'Discard changes' }));
  expect(screen.getByRole('textbox', { name: 'CPU limit' })).toHaveValue('4');
  expect(save).toBeDisabled();
  expect(screen.queryByText('You have unsaved capacity changes.')).toBeNull();
  expect(fetch.mock.calls.map(([input]) => input).sort()).toEqual([
    '/api/v1/admin/image-cache', '/api/v1/admin/infrastructure', '/api/v1/admin/resource-profiles', '/api/v1/session',
  ]);
  expect(fetch.mock.calls.every(([, init]) => !init?.method)).toBe(true);
});

// A failed capacity save must keep the administrator's draft and permit a
// deliberate retry. Success resets the save state only after refreshed data.
test('retains failed capacity edits and confirms a successful retry', async () => {
  let attempts = 0;
  globalThis.fetch = jest.fn(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json(attempts === 2
      ? { ...status, version: 3, capacity: { ...status.capacity, cpuCount: 5 } } : status);
    if (input === '/api/v1/admin/resource-profiles') return json([]);
    if (input === '/api/v1/admin/capacity' && init?.method === 'PUT') {
      attempts++;
      return attempts === 1 ? new Response(null, { status: 503 }) : json(null);
    }
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><Infrastructure /></Providers>);
  const cpu = await screen.findByRole('textbox', { name: 'CPU limit' });
  await user.clear(cpu);
  await user.type(cpu, '5');
  await user.click(screen.getByRole('button', { name: 'Save capacity budget' }));
  expect(await screen.findByText('Unable to complete the request')).toBeVisible();
  expect(cpu).toHaveValue('5');
  expect(screen.queryByText('Capacity budget saved.')).toBeNull();
  await user.click(screen.getByRole('button', { name: 'Save capacity budget' }));
  expect(await screen.findByText('Capacity budget saved.')).toBeVisible();
  expect(screen.getByRole('button', { name: 'Save capacity budget' })).toBeDisabled();
  expect(attempts).toBe(2);
});

// Operators can see both image architectures the Apple Silicon host can launch.
test('shows AMD64 and native ARM64 provider capabilities', async () => {
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json({
      ...status, provider: { ...status.provider, architectures: ['amd64', 'arm64'] },
    });
    throw new Error('unexpected request');
  });
  render(<Providers><Infrastructure /></Providers>);
  expect(await screen.findByText('x86-64 (amd64), ARM64')).toBeVisible();
});

// Retention starts at the server default and the edited value survives a new page load.
test('saves launch log retention and reads the saved value on reload', async () => {
  let launchLogRetention = { days: 30, version: 1 };
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json({ ...status, launchLogRetention });
    if (input === '/api/v1/admin/resource-profiles') return json([]);
    if (input === '/api/v1/admin/launch-log-retention' && init?.method === 'PUT') {
      launchLogRetention = { ...JSON.parse(String(init.body)), version: 2 };
      return json(null);
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  const page = render(<Providers><Infrastructure /></Providers>);
  const input = await screen.findByRole('textbox', { name: 'Retention days' });
  expect(input).toHaveValue('30');
  await user.clear(input);
  await user.type(input, '14');
  await user.click(screen.getByRole('button', { name: 'Save log retention' }));
  expect(await screen.findByText('Launch log retention saved.')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/launch-log-retention', expect.objectContaining({
    body: JSON.stringify({ days: 14 }),
    headers: expect.objectContaining({ 'If-Match': '"1"', 'X-CSRF-Token': 'fixture' }),
  }));
  page.unmount();
  render(<Providers><Infrastructure /></Providers>);
  expect(await screen.findByRole('textbox', { name: 'Retention days' })).toHaveValue('14');
});

const officialImage = { releaseId: 'v1.4.2-rc.1', region: 'us-east-2', amiId: 'ami-official' };

// The uploaded official manifest must reach the controller byte for byte,
// and the existing canary workflow must show a durable validation failure.
test('submits the authorized official catalog and shows canary failure', async () => {
  const raw = '{\n  "builds": [{ "artifact_id": "us-east-2:ami-fixture" }]\n}\n';
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json({ ...status, provider: { name: 'ec2', officialImage } });
    if (input === '/api/v1/admin/resource-profiles') return json([]);
    if (input === '/api/v1/templates/catalog') return json({ platformImages: [] });
    if (input === '/api/v1/admin/platform-artifacts' && init?.method === 'POST') return json({ id: 'official-operation' });
    if (input === '/api/v1/operations/official-operation') return json({
      id: 'official-operation', status: 'failed',
      steps: [{ key: 'import', index: 0, label: 'Import and verify the platform image', status: 'failed',
        detail: 'The disposable image boot did not authenticate.' }],
    });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  const page = () => <Providers><MantineProvider env="test"><Infrastructure /></MantineProvider></Providers>;
  const mounted = render(page());
  const panel = await screen.findByRole('region', { name: 'Official AWS release' });
  expect(within(panel).getByText('v1.4.2-rc.1 · us-east-2')).toBeVisible();
  const input = panel.querySelector<HTMLInputElement>('input[type=file]');
  if (!input) throw new Error('Expected the official catalog file picker');
  const file = new File([raw], 'us-east-2.json', { type: 'application/json' });
  Object.defineProperty(file, 'text', { value: async () => raw });
  await user.upload(input, file);
  await user.click(within(panel).getByRole('button', { name: 'Validate official AMI' }));
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v1/admin/platform-artifacts', expect.objectContaining({
    method: 'POST', headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture' }),
    body: JSON.stringify({ officialManifest: raw }),
  })));
  expect(await within(panel).findByText('AMI validation failed')).toBeVisible();
  expect(within(panel).getByText('The disposable image boot did not authenticate.')).toBeVisible();
  mounted.unmount();
  render(page());
  expect(await screen.findByText('AMI validation failed')).toBeVisible();
  window.sessionStorage.clear();
});

// A read-only administrator sees a validated catalog entry after loading
// from the server, with no controls that can submit another import.
test('shows a previously validated official release without import permission', async () => {
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json({ ...session, permissions: ['infrastructure.view'] });
    if (input === '/api/v1/admin/infrastructure') return json({ ...status, provider: { name: 'ec2', officialImage } });
    if (input === '/api/v1/admin/resource-profiles') return json([]);
    if (input === '/api/v1/templates/catalog') return json({ platformImages: [
      { id: 'official-image', name: officialImage.releaseId, providerKind: 'ec2', architecture: 'amd64', amiId: officialImage.amiId },
    ] });
    throw new Error('unexpected request');
  });
  render(<Providers><MantineProvider env="test"><Infrastructure /></MantineProvider></Providers>);
  const panel = within(await screen.findByRole('region', { name: 'Official AWS release' }));
  expect(await panel.findByText('Validated')).toBeVisible();
  expect(panel.queryByText('Official AMI catalog manifest')).not.toBeInTheDocument();
  expect(panel.queryByRole('button', { name: 'Validate official AMI' })).not.toBeInTheDocument();
});
