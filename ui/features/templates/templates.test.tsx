import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { Templates } from './templates';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

const project = { id: 'project-id', name: 'Example project', ownerId: 'someone-else', ownerName: 'Project author', state: 'active',
  currentRevision: 2, version: 4, repositoryUrl: 'https://github.com/example/project', repositoryRef: 'main',
  devcontainerPath: '.devcontainer/devcontainer.json', resourceProfileId: 'profile-id', resourceProfileName: 'Small',
  platformArtifactId: 'image-id', platformImageName: 'Ubuntu release', secretNames: ['API_KEY'] };
const session = { user: { id: 'member', role: 'member' }, mode: 'normal', installationState: 'active', permissions: [], csrfToken: 'fixture' };
const json = (data: unknown) => ({ ok: true, status: 200, json: async () => data }) as Response;

// Shared users can inspect readable configuration without gaining edit or
// secret-management controls, and closing details returns keyboard focus.
test('shows named project details and respects shared access', async () => {
  globalThis.fetch = jest.fn(async input => json(input === '/api/v1/session' ? session : { items: [project], nextCursor: null }));
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  const view = await screen.findByRole('button', { name: 'View Example project' });
  expect(screen.getByText('Small')).toBeVisible();
  expect(screen.getByText('Ubuntu release')).toBeVisible();
  expect(screen.getByText('Project author')).toBeVisible();
  expect(screen.queryByText('profile-id')).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /Actions for/ })).not.toBeInTheDocument();
  const launch = screen.getByRole('button', { name: 'Launch workspace for Example project' });
  expect(within(launch).getByText('Launch workspace', { exact: true })).toBeVisible();
  await user.click(launch);
  const review = await screen.findByRole('dialog', { name: 'Review launch' });
  await waitFor(() => expect(within(review).getByRole('textbox', { name: 'Workspace name' })).toHaveFocus());
  await user.keyboard('{Escape}');
  await waitFor(() => expect(launch).toHaveFocus());
  await user.click(view);
  const details = screen.getByRole('dialog', { name: 'Project details' });
  expect(within(details).getByRole('button', { name: 'Close project details' })).toBeVisible();
  expect(within(details).getByText('.devcontainer/devcontainer.json')).toBeVisible();
  expect(within(details).getByText('API_KEY')).toBeVisible();
  await user.click(within(details).getByRole('button', { name: 'Done' }));
  await waitFor(() => expect(view).toHaveFocus());
});

// Live search includes repositories and preserves the query when switching to archived projects.
test('shares the search across views and restricts archived projects to details and management', async () => {
  const archived = { ...project, id: 'archived', name: 'Archived example', state: 'archived' };
  const other = { ...project, id: 'other', name: 'Different project', repositoryUrl: 'https://github.com/other/repo' };
  const fetch = jest.fn(async (input: RequestInfo | URL) => {
    if (input === '/api/v1/session') return json({ ...session, user: { id: 'member', role: 'administrator' } });
    if (input === '/api/v1/templates?archived=true') return json({ items: [archived], nextCursor: null });
    return json({ items: [project, other], nextCursor: null });
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  await screen.findByRole('button', { name: 'Launch workspace for Different project' });
  const search = screen.getByRole('searchbox', { name: 'Search projects' });
  await user.type(search, 'example/project');
  expect(screen.queryByRole('button', { name: 'Launch workspace for Different project' })).not.toBeInTheDocument();
  await user.click(screen.getByRole('radio', { name: 'Table' }));
  const table = within(screen.getByRole('table', { name: 'Projects' }));
  expect(table.getAllByRole('row')).toHaveLength(2);
  expect(table.getByRole('button', { name: 'Launch workspace for Example project' })).toHaveTextContent('Launch workspace');
  await user.click(screen.getByRole('switch', { name: 'Archived projects' }));
  expect(await screen.findByRole('button', { name: 'View Archived example' })).toBeVisible();
  expect(screen.queryByRole('button', { name: /Launch workspace for/ })).not.toBeInTheDocument();
  expect(search).toHaveValue('example/project');
  await user.click(screen.getByRole('button', { name: 'Clear search' }));
  expect(search).toHaveValue('');
  expect(fetch).toHaveBeenCalledWith('/api/v1/templates?archived=true', expect.anything());
});

// A failed list can be retried without reloading the app; a valid empty
// search explains that the filter, rather than missing access, found no rows.
test('recovers from list errors and explains empty search results', async () => {
  let fail = true;
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (fail) throw new Error('Offline');
    return json({ items: [], nextCursor: null });
  });
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  const retry = await screen.findByRole('button', { name: 'Retry' });
  fail = false;
  await user.click(retry);
  expect(await screen.findByText('No projects yet.')).toBeVisible();
  await user.type(screen.getByRole('searchbox', { name: 'Search projects' }), 'missing{Enter}');
  expect(await screen.findByText('No projects match your search.')).toBeVisible();
});

// A match on a later API page appears in both layouts, even when the user starts on a different table page.
test('loads the full catalog and resets table pagination for a new search', async () => {
  const items = Array.from({ length: 10 }, (_, index) => ({ ...project, id: `project-${index}`, name: `Project ${index}` }));
  const second = { ...project, id: 'second', name: 'Second project', currentRevision: 3 };
  globalThis.fetch = jest.fn(async input => {
    const responses: Record<string, unknown> = {
      '/api/v1/session': session,
      '/api/v1/templates': { items, nextCursor: 'after-one' },
      '/api/v1/templates?cursor=after-one': { items: [second], nextCursor: null },
    };
    if (typeof input !== 'string' || !(input in responses)) throw new Error('unexpected request');
    return { ok: true, status: 200, json: async () => responses[input] } as Response;
  });
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  expect(await screen.findByRole('button', { name: 'Launch workspace for Second project' })).toBeVisible();
  expect(screen.getByText('Revision 3')).toBeVisible();
  await user.click(screen.getByRole('radio', { name: 'Table' }));
  expect(screen.queryByRole('button', { name: 'Launch workspace for Second project' })).not.toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: 'Next page' }));
  expect(screen.getByRole('button', { name: 'Launch workspace for Second project' })).toBeVisible();
  await user.type(screen.getByRole('searchbox', { name: 'Search projects' }), 'Project 0');
  expect(screen.getByRole('button', { name: 'Launch workspace for Project 0' })).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Launch workspace for Second project' })).not.toBeInTheDocument();
});

// An installation default is preselected and saved when creating a template.
test('uses the installation platform default for a new project', async () => {
  const fetch = jest.fn(async (path: RequestInfo | URL, init?: RequestInit) => {
    if (path === '/api/v1/session') return json({ ...session, permissions: ['template.create'] });
    if (path === '/api/v1/templates/catalog') return json({
      resourceProfiles: [{ id: 'profile-id', name: 'Small' }],
      platformImages: [{ id: 'amd-image', name: 'AMD64' }, { id: 'arm-image', name: 'ARM64' }],
      defaultPlatformArtifactId: 'arm-image',
    });
    if (init?.method === 'POST') return json({ id: 'new-template' });
    return json({ items: [] });
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'New project' }));
  const dialog = within(screen.getByRole('dialog', { name: 'New project' }));
  const image = await dialog.findByRole('combobox', { name: 'Platform image' });
  expect(image).toHaveValue('arm-image');
  await user.type(dialog.getByRole('textbox', { name: /^Name/ }), 'Native project');
  await user.type(dialog.getByRole('textbox', { name: 'Repository URL' }), project.repositoryUrl);
  await user.selectOptions(dialog.getByRole('combobox', { name: 'Resource profile' }), 'profile-id');
  await user.click(dialog.getByRole('button', { name: 'Create project' }));
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v1/templates', expect.objectContaining({
    method: 'POST', body: expect.stringContaining('"platformArtifactId":"arm-image"'),
  })));
});

// AWS template authors select only a resource profile; its AMI remains owned and resolved by the profile.
test('creates an AWS project without selecting or submitting an AMI', async () => {
  const fetch = jest.fn(async (path: RequestInfo | URL, init?: RequestInit) => {
    if (path === '/api/v1/session') return json({ ...session, permissions: ['template.create'] });
    if (path === '/api/v1/templates/catalog') return json({ providerKind: 'ec2',
      resourceProfiles: [{ id: 'aws-profile', name: 'AWS Small', providerKind: 'ec2', platformArtifactId: 'profile-image' }],
      platformImages: [],
    });
    if (init?.method === 'POST') return json({ id: 'new-template' });
    return json({ items: [] });
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'New project' }));
  const dialog = within(screen.getByRole('dialog', { name: 'New project' }));
  await dialog.findByRole('combobox', { name: 'Resource profile' });
  expect(dialog.queryByRole('combobox', { name: 'Platform image' })).not.toBeInTheDocument();
  await user.type(dialog.getByRole('textbox', { name: /^Name/ }), 'AWS project');
  await user.type(dialog.getByRole('textbox', { name: 'Repository URL' }), project.repositoryUrl);
  await user.selectOptions(dialog.getByRole('combobox', { name: 'Resource profile' }), 'aws-profile');
  await user.click(dialog.getByRole('button', { name: 'Create project' }));
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v1/templates', expect.objectContaining({ method: 'POST' })));
  const request = fetch.mock.calls.find(([path, init]) => path === '/api/v1/templates' && init?.method === 'POST');
  expect(JSON.parse(String(request?.[1]?.body))).toMatchObject({ resourceProfileId: 'aws-profile' });
  expect(JSON.parse(String(request?.[1]?.body))).not.toHaveProperty('platformArtifactId');
});
