import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { Templates } from './templates';

const originalFetch = globalThis.fetch;
beforeEach(() => {
  // jsdom has no layout; give Mantine a visible anchor and viewport so
  // its detached-anchor detection does not hide an open menu.
  jest.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
    x: 0, y: 0, top: 0, left: 0, right: 100, bottom: 40, width: 100, height: 40, toJSON: () => ({}),
  });
  jest.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(1024);
  jest.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(768);
});
afterEach(() => { globalThis.fetch = originalFetch; jest.restoreAllMocks(); });

const project = { id: 'template', ownerId: 'owner', name: 'Sample', state: 'active', version: 4,
  currentRevision: 2, repositoryUrl: 'https://github.com/example/sample', resourceProfileId: 'small',
  platformArtifactId: 'platform', launchGrants: [] };
const memberID = 'a1234567-1234-4321-8765-123456789abc';
const fixture = (role: string, fetch: (path: string, init?: RequestInit) => unknown) => {
  globalThis.fetch = jest.fn(async (path: string, init?: RequestInit) => {
    const data = path === '/api/v1/session'
      ? { user: { id: 'owner', role }, mode: 'normal', installationState: 'active', permissions: ['template.create'], csrfToken: 'fixture' }
      : fetch(path, init);
    return { ok: true, status: 200, json: async () => data } as Response;
  }) as unknown as typeof globalThis.fetch;
};

// A new template cannot be submitted until the installation has both choices.
test('explains when no platform image is available', async () => {
  fixture('member', path => path === '/api/v1/templates/catalog'
    ? { resourceProfiles: [{ id: 'small', name: 'Small' }], platformImages: [] }
    : { items: [] });
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'New template' }));
  const dialog = screen.getByRole('dialog', { name: 'New template' });
  expect(await within(dialog).findByText(/an administrator must configure/i)).toBeVisible();
  expect(within(dialog).getByRole('button', { name: 'Create template' })).toBeDisabled();
});

// A template owner must explicitly acknowledge project secret access
// before a grant can be submitted. Members never get the archive filter.
test('requires project secret acknowledgement when sharing a template', async () => {
  fixture('member', path => path === '/api/v1/templates' ? { items: [project] } : null);
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Actions for Sample' }));
  await user.click(await screen.findByRole('menuitem', { name: 'Share launch access' }));
  const dialog = screen.getByRole('dialog', { name: 'Share launch access' });
  expect(within(dialog).getByText(/workspace code can retrieve project secrets/i)).toBeVisible();
  const submit = within(dialog).getByRole('button', { name: 'Grant launch access' });
  await user.type(within(dialog).getByLabelText('User ID'), memberID);
  expect(submit).toBeDisabled();
  await user.click(within(dialog).getByRole('checkbox', { name: 'I acknowledge project secret access' }));
  await user.click(submit);
  expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/templates/template/grants', expect.objectContaining({
    method: 'POST',
    headers: expect.objectContaining({ 'Idempotency-Key': expect.any(String), 'X-CSRF-Token': 'fixture' }),
    body: JSON.stringify({ userId: memberID, acknowledgeProjectSecretExposure: true }),
  }));
  expect(screen.queryByRole('switch', { name: 'Archived templates' })).not.toBeInTheDocument();
});

// Administrators explicitly switch to the archived list before restoring
// a template. Restoring sends the version the administrator reviewed.
test('filters archived templates and restores with the reviewed version', async () => {
  let archived = true;
  fixture('administrator', (path, init) => {
    if (path === '/api/v1/templates/template' && init?.method === 'PATCH') {
      archived = false;
      return null;
    }
    if (path === '/api/v1/templates?archived=true') return { items: archived ? [{ ...project, state: 'archived' }] : [] };
    return { items: [] };
  });
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  await screen.findByText('No templates yet.');
  expect(globalThis.fetch).not.toHaveBeenCalledWith('/api/v1/templates?archived=true', expect.anything());
  await user.click(screen.getByRole('switch', { name: 'Archived templates' }));
  await user.click(await screen.findByRole('button', { name: 'Actions for Sample' }));
  await user.click(await screen.findByRole('menuitem', { name: 'Restore template' }));
  const dialog = screen.getByRole('dialog', { name: 'Restore template' });
  await user.click(within(dialog).getByRole('button', { name: 'Restore template' }));
  expect(await screen.findByText('No archived templates.')).toBeVisible();
  expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/templates/template', expect.objectContaining({
    method: 'PATCH', body: '{"state":"active"}', headers: expect.objectContaining({ 'If-Match': '"4"' }),
  }));
});

// Revising a template preserves its current fields and submits the observed
// version so a concurrent edit cannot silently replace the current revision.
test('creates a new revision from the reviewed template', async () => {
  let current = project;
  fixture('member', (path, init) => {
    if (path === '/api/v1/templates/catalog') return {
      resourceProfiles: [{ id: 'small', name: 'Small' }],
      platformImages: [{ id: 'platform', name: 'Ubuntu release' }, { id: 'next-platform', name: 'Next Ubuntu release' }],
      defaultPlatformArtifactId: 'next-platform',
    };
    if (path.endsWith('/revisions') && init?.method === 'POST') {
      current = { ...project, currentRevision: 3 };
      return current;
    }
    return { items: [current] };
  });
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Actions for Sample' }));
  await user.click(await screen.findByRole('menuitem', { name: 'New revision' }));
  const dialog = screen.getByRole('dialog', { name: 'New revision' });
  expect(within(dialog).getByLabelText(/^Repository URL/)).toHaveValue(project.repositoryUrl);
  const profile = await within(dialog).findByRole('combobox', { name: 'Resource profile' });
  const image = within(dialog).getByRole('combobox', { name: 'Platform image' });
  expect(await within(profile).findByRole('option', { name: 'Small' })).toHaveProperty('selected', true);
  expect(within(image).getByRole('option', { name: 'Ubuntu release' })).toHaveProperty('selected', true);
  await user.selectOptions(image, within(image).getByRole('option', { name: 'Next Ubuntu release' }));
  await user.type(within(dialog).getByLabelText('Repository ref'), 'next');
  await user.click(within(dialog).getByRole('button', { name: 'Save revision' }));
  expect(await screen.findByText('Revision 3')).toBeVisible();
  expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/templates/template/revisions', expect.objectContaining({
    method: 'POST', headers: expect.objectContaining({ 'If-Match': '"4"', 'Idempotency-Key': expect.any(String) }),
  }));
  const body = (globalThis.fetch as jest.Mock).mock.calls.find(([, init]) => init?.method === 'POST')?.[1]?.body;
  expect(JSON.parse(body)).toMatchObject({ resourceProfileId: 'small', platformArtifactId: 'next-platform' });
});

// A template manager can revoke a previously granted user's access from
// the server's list. Revocation waits for an explicit click.
test('revokes an existing launch grant from the sharing dialog', async () => {
  let grants = [{ userId: memberID, email: 'member@example.test' }];
  fixture('member', (path, init) => {
    if (path.endsWith(memberID) && init?.method === 'DELETE') {
      grants = [];
      return null;
    }
    return { items: [{ ...project, launchGrants: grants }] };
  });
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Actions for Sample' }));
  await user.click(await screen.findByRole('menuitem', { name: 'Share launch access' }));
  expect(screen.getByText('member@example.test')).toBeVisible();
  await user.click(screen.getByRole('button', { name: 'Revoke launch access for member@example.test' }));
  expect(globalThis.fetch).toHaveBeenCalledWith('/api/v1/templates/template/grants/' + memberID, expect.objectContaining({
    method: 'DELETE', headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture' }),
  }));
  expect(await screen.findByText('No launch grants.')).toBeVisible();
});
