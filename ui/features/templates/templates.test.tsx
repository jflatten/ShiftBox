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
test('shows named template details and respects shared access', async () => {
  globalThis.fetch = jest.fn(async input => json(input === '/api/v1/session' ? session : { items: [project], nextCursor: null }));
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  const view = await screen.findByRole('button', { name: 'View Example project' });
  expect(screen.getByText('Small')).toBeVisible();
  expect(screen.getByText('Ubuntu release')).toBeVisible();
  expect(screen.getByText('Project author')).toBeVisible();
  expect(screen.queryByText('profile-id')).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: /Actions for/ })).not.toBeInTheDocument();
  await user.click(view);
  const details = screen.getByRole('dialog', { name: 'Template details' });
  expect(within(details).getByRole('button', { name: 'Close template details' })).toBeVisible();
  expect(within(details).getByText('.devcontainer/devcontainer.json')).toBeVisible();
  expect(within(details).getByText('API_KEY')).toBeVisible();
  await user.click(within(details).getByRole('button', { name: 'Done' }));
  await waitFor(() => expect(view).toHaveFocus());
});

// Search is sent to the API before paging, preserves the archived view, and
// resets a previous page cursor so matching templates are not skipped.
test('searches across pages and preserves archive scope', async () => {
  const fetch = jest.fn(async (input: RequestInfo | URL) => json(input === '/api/v1/session'
    ? { ...session, user: { id: 'member', role: 'administrator' } }
    : { items: [project], nextCursor: 'next' }));
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  await screen.findByRole('button', { name: 'View Example project' });
  await user.click(screen.getByRole('button', { name: 'Next page' }));
  await screen.findByText('Page 2');
  await user.type(screen.getByRole('searchbox', { name: 'Search templates' }), 'Example{Enter}');
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v1/templates?q=Example', expect.anything()));
  expect(screen.getByRole('button', { name: 'Previous page' })).toBeDisabled();
  await user.click(screen.getByRole('switch', { name: 'Archived templates' }));
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v1/templates?archived=true&q=Example', expect.anything()));
  await user.click(screen.getByRole('button', { name: 'Clear search' }));
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v1/templates?archived=true', expect.anything()));
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
  expect(await screen.findByText('No templates yet.')).toBeVisible();
  await user.type(screen.getByRole('searchbox', { name: 'Search templates' }), 'missing{Enter}');
  expect(await screen.findByText('No templates match your search.')).toBeVisible();
});

// Templates shows only the current server page and follows the cursor when
// a user's permitted templates span more than one page.
test('lists current template revisions and follows the server cursor', async () => {
  globalThis.fetch = jest.fn(async input => {
    const responses: Record<string, unknown> = {
      '/api/v1/session': { user: { id: 'member', role: 'member' }, mode: 'normal', installationState: 'active', permissions: [], csrfToken: 'fixture' },
      '/api/v1/templates': { items: [{ id: 'one', name: 'Example project', currentRevision: 2 }], nextCursor: 'after-one' },
      '/api/v1/templates?cursor=after-one': { items: [{ id: 'two', name: 'Second project', currentRevision: 1 }], nextCursor: null },
    };
    if (typeof input !== 'string' || !(input in responses)) throw new Error('unexpected request');
    return { ok: true, status: 200, json: async () => responses[input] } as Response;
  });
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  expect(await screen.findByRole('button', { name: 'View Example project' })).toBeVisible();
  expect(screen.getByText('Revision 2')).toBeVisible();
  await user.click(screen.getByRole('button', { name: 'Next page' }));
  expect(await screen.findByRole('button', { name: 'View Second project' })).toBeVisible();
  expect(screen.queryByRole('button', { name: 'View Example project' })).toBeNull();
});

// An installation default is preselected and saved when creating a template.
test('uses the installation platform default for a new template', async () => {
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
  await user.click(await screen.findByRole('button', { name: 'New template' }));
  const dialog = within(screen.getByRole('dialog', { name: 'New template' }));
  const image = await dialog.findByRole('combobox', { name: 'Platform image' });
  expect(image).toHaveValue('arm-image');
  await user.type(dialog.getByRole('textbox', { name: /^Name/ }), 'Native project');
  await user.type(dialog.getByRole('textbox', { name: 'Repository URL' }), project.repositoryUrl);
  await user.selectOptions(dialog.getByRole('combobox', { name: 'Resource profile' }), 'profile-id');
  await user.click(dialog.getByRole('button', { name: 'Create template' }));
  await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v1/templates', expect.objectContaining({
    method: 'POST', body: expect.stringContaining('"platformArtifactId":"arm-image"'),
  })));
});
