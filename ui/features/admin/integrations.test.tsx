import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { AdministrationIntegrations } from './integrations';

const originalFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = originalFetch;
  window.history.replaceState(null, '', '/');
});

const session = {
  user: { id: 'operator', role: 'administrator' }, mode: 'normal', installationState: 'active',
  permissions: ['infrastructure.view', 'infrastructure.manage', 'secret.manage_organization'],
  organizationSecretScopeId: 'organization', csrfToken: 'fixture',
};

function json(payload: unknown) {
  return { ok: true, status: 200, json: async () => payload } as Response;
}

// The admin starts registration with one ShiftBox action, then sees the
// persisted installation state on returning from GitHub and after a reload.
// Setup steps are visible only until repository access is verified.
test('sets up a GitHub App from Administration Integrations', async () => {
  const endpoint = '/api/v1/admin/settings/github-app';
  let state: 'not_configured' | 'installation_pending' | 'ready' = 'not_configured';
  const submit = jest.spyOn(HTMLFormElement.prototype, 'submit').mockImplementation(function (this: HTMLFormElement) {
    expect(this.method).toBe('post');
    expect(this.action).toBe('https://github.com/organizations/sample-org/settings/apps/new?state=fixture');
    expect(new FormData(this).get('manifest')).toBe('{"public":false}');
  });
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json({ conditions: [] });
    if (input === endpoint && init?.method === 'POST') return json({
      action: 'https://github.com/organizations/sample-org/settings/apps/new?state=fixture',
      manifest: '{"public":false}',
    });
    if (input === endpoint+'/callback') {
      state = 'installation_pending';
      return json({ state, appName: 'ShiftBox Example', owner: 'sample-org' });
    }
    if (input === endpoint+'/verify') {
      state = 'ready';
      return json({ state, appName: 'ShiftBox Example', owner: 'sample-org', repositorySelection: 'selected' });
    }
    if (input === endpoint) return json({ state, appName: 'ShiftBox Example', owner: 'sample-org' });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  try {
    window.history.replaceState(null, '', '/integrations/');
    const startPage = render(<Providers><AdministrationIntegrations /></Providers>);
    expect(await screen.findByRole('heading', { name: 'Integrations' })).toBeVisible();
    expect(await screen.findByRole('region', { name: 'GitHub Integration' })).toBeVisible();
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
    expect(await screen.findByText('Register app', { exact: true })).toBeVisible();
    expect(screen.getByRole('link', { name: 'Integrations' })).toHaveAttribute('href', '/integrations/');
    await user.type(await screen.findByRole('textbox', { name: 'GitHub organization' }), 'sample-org');
    await user.click(screen.getByRole('button', { name: 'Set up GitHub App' }));
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1));
    expect(fetch).toHaveBeenCalledWith(endpoint, expect.objectContaining({
      method: 'POST', headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture' }),
      body: JSON.stringify({ organization: 'sample-org', restart: false }),
    }));
    startPage.unmount();
    window.history.replaceState(null, '', '/integrations/#github-code=fixture&github-state=registration');
    const registrationPage = render(<Providers><AdministrationIntegrations /></Providers>);
    expect(await screen.findByText('Repository access pending')).toBeVisible();
    expect(screen.getByText('Select repositories', { exact: true })).toBeVisible();
    expect(fetch).toHaveBeenCalledWith(endpoint+'/callback', expect.objectContaining({
      method: 'POST', headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture' }),
      body: JSON.stringify({ code: 'fixture', state: 'registration' }),
    }));
    expect(window.location.hash).toBe('');
    registrationPage.unmount();
    window.history.replaceState(null, '', '/integrations/?github-install=1&installation_id=18&setup_action=install');
    const returnPage = render(<Providers><AdministrationIntegrations /></Providers>);
    expect(await screen.findByText('Workspace users can now connect their GitHub accounts.')).toBeVisible();
    expect(screen.queryByText('Ready for users', { exact: true })).not.toBeInTheDocument();
    expect(fetch).toHaveBeenCalledWith(endpoint+'/verify', expect.objectContaining({ method: 'POST' }));
    returnPage.unmount();
    expect(window.location.search).toBe('');
    window.history.replaceState(null, '', '/integrations/');
    render(<Providers><AdministrationIntegrations /></Providers>);
    expect(await screen.findByText('Workspace users can now connect their GitHub accounts.')).toBeVisible();
    const integration = within(screen.getByRole('region', { name: 'GitHub Integration' }));
    expect(integration.getByText('Application Name: ShiftBox Example')).toBeVisible();
    expect(integration.getByText('Owner: sample-org')).toBeVisible();
    expect(integration.getByText('Ready', { exact: true })).toBeVisible();
    expect(integration.queryByText('Register app', { exact: true })).not.toBeInTheDocument();
    expect(fetch.mock.calls.some(([input]) => input === '/api/v1/git/authorization')).toBe(false);
  } finally {
    submit.mockRestore();
  }
});

// Direct navigation cannot load setup data for a member or a settings-only administrator.
test.each([
  { role: 'member', permissions: ['infrastructure.manage'] },
  { role: 'administrator', permissions: ['infrastructure.view', 'secret.manage_organization'] },
])('denies Integrations to a $role without integration access', async ({ role, permissions }) => {
  const fetch = jest.fn(async () => json({ ...session, user: { id: 'viewer', role }, permissions }));
  globalThis.fetch = fetch;
  render(<Providers><AdministrationIntegrations /></Providers>);
  expect(await screen.findByText('Administrator access required.')).toBeVisible();
  expect(screen.queryByRole('link', { name: 'Integrations' })).not.toBeInTheDocument();
  expect(screen.queryByRole('region', { name: 'GitHub Integration' })).not.toBeInTheDocument();
  expect(fetch.mock.calls).toEqual([['/api/v1/session', expect.any(Object)]]);
});

// Cancel keeps repository access intact. Confirm targets the displayed installation
// and a successful revocation keeps the app available for reinstall after reload.
test('confirms before revoking GitHub repository access', async () => {
  const endpoint = '/api/v1/admin/settings/github-app';
  const registered = { appName: 'ShiftBox Example', owner: 'sample-org' };
  let setup = { ...registered, state: 'ready', installationId: '4321',
    manageUrl: 'https://github.com/organizations/sample-org/settings/installations/4321', installUrl: '' };
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/admin/infrastructure') return json({ conditions: [] });
    if (input === endpoint+'/revoke' && init?.method === 'POST') {
      setup = { ...registered, state: 'installation_pending', installationId: '', manageUrl: '',
        installUrl: 'https://github.com/apps/shiftbox-example/installations/new' };
      return json(setup);
    }
    if (input === endpoint) return json(setup);
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  const page = render(<Providers><AdministrationIntegrations /></Providers>);
  const revoke = await screen.findByRole('button', { name: 'Revoke access' });
  await user.click(revoke);
  const confirmation = within(await screen.findByRole('dialog', { name: 'Revoke GitHub access?' }));
  expect(confirmation.getByText(/removes the GitHub App installation/)).toBeVisible();
  expect(confirmation.getByRole('button', { name: 'Cancel' })).toHaveFocus();
  await user.click(confirmation.getByRole('button', { name: 'Cancel' }));
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  await waitFor(() => expect(revoke).toHaveFocus());
  expect(fetch.mock.calls.some(([input]) => input === endpoint+'/revoke')).toBe(false);
  await user.click(revoke);
  await user.click(within(await screen.findByRole('dialog', { name: 'Revoke GitHub access?' }))
    .getByRole('button', { name: 'Revoke access' }));
  expect(await screen.findByText('Repository access pending')).toBeVisible();
  expect(fetch).toHaveBeenCalledWith(endpoint+'/revoke', expect.objectContaining({
    method: 'POST', body: JSON.stringify({ installationId: '4321' }),
    headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture' }),
  }));
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  page.unmount();
  render(<Providers><AdministrationIntegrations /></Providers>);
  expect(await screen.findByRole('link', { name: 'Select repositories on GitHub' })).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Revoke access' })).not.toBeInTheDocument();
});
