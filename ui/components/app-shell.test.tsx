import { MantineProvider } from '@mantine/core';
import { act, render, screen, within } from '@testing-library/react';
import { ShiftBoxShell } from './app-shell';
import userEvent from '@testing-library/user-event';
import { Providers } from '../app/providers';

beforeEach(() => {
  // jsdom has no layout; menu anchors need a visible viewport.
  jest.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
    x: 0, y: 0, top: 0, left: 0, right: 100, bottom: 40, width: 100, height: 40, toJSON: () => ({}),
  });
  jest.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(1024);
  jest.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(768);
});
afterEach(() => jest.restoreAllMocks());

// A member must not be offered administration navigation by the shared shell.
test('shows member navigation and the approved brand', () => {
  render(<MantineProvider><ShiftBoxShell session={{ role: 'member', mode: 'normal', installationState: 'active', permissions: ['template.create'] }} alerts={[]}><h1>Workspaces</h1></ShiftBoxShell></MantineProvider>);
  expect(screen.getByRole('link', { name: 'Workspaces' })).toBeVisible();
  expect(screen.queryByRole('link', { name: 'Infrastructure' })).not.toBeInTheDocument();
  expect(screen.getByAltText('ShiftBox')).toHaveAttribute('src', '/brand/shiftbox-logo-light.svg');
});

// Restricted administrator sessions must not retain the navigation of a normal active session.
test('respects session gates and keeps persistent conditions visible', () => {
  render(<MantineProvider><ShiftBoxShell session={{ role: 'administrator', mode: 'password_change', installationState: 'active', permissions: ['session.change_password'] }} alerts={[{ id: 'password', title: 'Change your password', detail: 'Choose a new password to continue.' }]}><h1>Account</h1></ShiftBoxShell></MantineProvider>);
  expect(screen.queryByRole('link', { name: 'Workspaces' })).not.toBeInTheDocument();
  expect(screen.queryByRole('link', { name: 'Infrastructure' })).not.toBeInTheDocument();
  expect(screen.getByRole('alert')).toHaveTextContent('Choose a new password to continue.');
  expect(screen.getByRole('region', { name: 'Persistent conditions' })).toContainElement(screen.getByRole('alert'));
  expect(screen.queryByRole('button', { name: /close/i })).not.toBeInTheDocument();
});

// Administrative navigation needs the server's current permission, not the role name alone.
test('shows permitted administration navigation', () => {
  render(<MantineProvider><ShiftBoxShell session={{ role: 'administrator', mode: 'normal', installationState: 'active', permissions: ['infrastructure.view'] }} alerts={[]}><h1>Overview</h1></ShiftBoxShell></MantineProvider>);
  expect(screen.getByRole('link', { name: 'Infrastructure' })).toBeVisible();
  expect(screen.queryByRole('link', { name: 'Users' })).not.toBeInTheDocument();
});

// The approved Users navigation label points to the membership route.
test('links authorized administrators to Users', () => {
  render(<MantineProvider><ShiftBoxShell session={{ role: 'administrator', mode: 'normal', installationState: 'active', permissions: ['user.list'] }} alerts={[]}><h1>Overview</h1></ShiftBoxShell></MantineProvider>);
  expect(screen.getByRole('link', { name: 'Users' })).toHaveAttribute('href', '/users/');
});

// Restored installations expose the review link while hiding normal controls.
test('links normal administrators and recovery reviewers to Backup & Recovery', () => {
  const session = { role: 'administrator' as const, mode: 'normal', installationState: 'active', permissions: ['backup.create'] };
  const shell = ({ mode, installationState, permissions }: typeof session) =>
    <MantineProvider><ShiftBoxShell session={{ ...session, mode, installationState, permissions }} alerts={[]}><h1>Overview</h1></ShiftBoxShell></MantineProvider>;
  const { rerender } = render(shell(session));
  expect(screen.getByRole('link', { name: 'Backup & Recovery' })).toHaveAttribute('href', '/recovery/');
  rerender(shell({ ...session, mode: 'recovery_review', installationState: 'recovery', permissions: ['recovery.review'] }));
  expect(screen.getByRole('link', { name: 'Backup & Recovery' })).toBeVisible();
  expect(screen.queryByRole('link', { name: 'Workspaces' })).not.toBeInTheDocument();
});

// An overdue organizational session still offers renewal, while protected
// navigation and the page body wait for successful upstream authorization.
test('gates overdue organizational sessions', () => {
  render(<MantineProvider><ShiftBoxShell session={{
    role: 'member', mode: 'normal', installationState: 'active',
    permissions: ['session.external_reauthorize'], csrfToken: 'fixture',
    externalAuthorizationDueAt: '2000-01-01T00:00:00Z',
  }} alerts={[]}><h1>Private workspaces</h1></ShiftBoxShell></MantineProvider>);
  expect(screen.getByRole('button', { name: 'Renew authorization' })).toBeVisible();
  expect(screen.getByText('Organizational authorization expired')).toBeVisible();
  expect(screen.queryByRole('link', { name: 'Workspaces' })).not.toBeInTheDocument();
  expect(screen.queryByRole('heading', { name: 'Private workspaces' })).not.toBeInTheDocument();
});

// An open workspace page must stop displaying protected content at the
// organizational deadline even when no navigation or refetch occurs.
test('gates a mounted page when organizational authorization expires', () => {
  jest.useFakeTimers();
  try {
    const due = new Date(Date.now() + 1000).toISOString();
    render(<MantineProvider><ShiftBoxShell session={{
      role: 'member', mode: 'normal', installationState: 'active', csrfToken: 'fixture',
      permissions: ['session.external_reauthorize'], externalAuthorizationDueAt: due,
    }} alerts={[]}><h1>Private workspaces</h1></ShiftBoxShell></MantineProvider>);
    expect(screen.getByRole('heading', { name: 'Private workspaces' })).toBeVisible();
    act(() => jest.advanceTimersByTime(1000));
    expect(screen.getByText('Organizational authorization expired')).toBeVisible();
    expect(screen.queryByRole('heading', { name: 'Private workspaces' })).not.toBeInTheDocument();
  } finally { jest.useRealTimers(); }
});

// A password-change session can still sign out and retire its restricted
// cookie without completing the password form.
test('signs out a restricted session with its CSRF token', async () => {
  const original = globalThis.fetch;
  const fetch = jest.fn().mockResolvedValue({ ok: true, status: 204 });
  globalThis.fetch = fetch;
  try {
    const user = userEvent.setup();
    render(<Providers><ShiftBoxShell session={{
      role: 'member', mode: 'password_change', installationState: 'active',
      permissions: ['session.change_password', 'session.logout'], csrfToken: 'fixture',
    }} alerts={[]}><h1>Change password</h1></ShiftBoxShell></Providers>);
    const trigger = screen.getByRole('button', { name: 'User menu' });
    expect(screen.getByRole('banner')).toContainElement(trigger);
    expect(within(screen.getByRole('navigation')).queryByRole('button', { name: 'Sign out' })).toBeNull();
    await user.click(trigger);
    await user.click(await screen.findByRole('menuitem', { name: 'Sign out' }));
    expect(fetch).toHaveBeenCalledWith('/api/v1/session', expect.objectContaining({
      method: 'DELETE', headers: { 'X-CSRF-Token': 'fixture' },
    }));
    expect(await screen.findByRole('link', { name: 'Sign in' })).toBeVisible();
    expect(screen.queryByRole('heading', { name: 'Change password' })).not.toBeInTheDocument();
  } finally { globalThis.fetch = original; }
});

// The header menu identifies the current user without exposing internal IDs,
// and personal settings no longer appears in the primary navigation.
test('offers personal settings through the user avatar menu', async () => {
  const user = userEvent.setup();
  render(<MantineProvider><ShiftBoxShell session={{ role: 'member', mode: 'normal', installationState: 'active', permissions: [],
    user: { displayName: 'Alex Developer', primaryEmail: 'alex@example.test' },
  }} alerts={[]}><h1>Workspaces</h1></ShiftBoxShell></MantineProvider>);
  expect(within(screen.getByRole('navigation')).queryByRole('link', { name: /Account|Personal settings/ })).toBeNull();
  await user.click(screen.getByRole('button', { name: 'User menu' }));
  expect(await screen.findByText('Alex Developer')).toBeVisible();
  expect(screen.getByText('alex@example.test')).toBeVisible();
  expect(screen.getByRole('menuitem', { name: 'Personal settings' })).toHaveAttribute('href', '/account/');
  await user.keyboard('{Escape}');
  expect(screen.getByRole('button', { name: 'User menu' })).toHaveFocus();
});

// A failed sign-out must leave the private page intact and provide an error;
// retry retires the session only after the server accepts the request.
test('keeps the session when sign-out fails and supports retry', async () => {
  const original = globalThis.fetch;
  globalThis.fetch = jest.fn().mockRejectedValueOnce(new Error('Offline')).mockResolvedValue({ ok: true, status: 204 });
  try {
    const user = userEvent.setup();
    render(<Providers><ShiftBoxShell session={{ role: 'member', mode: 'normal', installationState: 'active',
      permissions: ['session.logout'], csrfToken: 'fixture',
    }} alerts={[]}><h1>Private page</h1></ShiftBoxShell></Providers>);
    await user.click(screen.getByRole('button', { name: 'User menu' }));
    await user.click(await screen.findByRole('menuitem', { name: 'Sign out' }));
    expect(await screen.findByRole('alert')).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Private page' })).toBeVisible();
    await user.click(screen.getByRole('menuitem', { name: 'Sign out' }));
    expect(await screen.findByRole('link', { name: 'Sign in' })).toBeVisible();
    expect(screen.queryByRole('button', { name: 'User menu' })).toBeNull();
  } finally { globalThis.fetch = original; }
});

// Daily navigation stays separate from permission-gated administration links.
test('groups permitted administrative destinations in one expandable section', async () => {
  const user = userEvent.setup();
  render(<MantineProvider><ShiftBoxShell session={{ role: 'administrator', mode: 'normal', installationState: 'active',
    permissions: ['notice.list', 'infrastructure.view', 'user.list', 'audit.list', 'backup.create'] }} alerts={[]}>
    <h1>Workspaces</h1>
  </ShiftBoxShell></MantineProvider>);
  const nav = screen.getByRole('navigation', { name: 'Main navigation' });
  const group = within(nav).getByRole('button', { name: 'Administration' });
  expect(group).toHaveAttribute('aria-expanded', 'true');
  for (const label of ['Notices', 'Infrastructure', 'Users', 'Audit', 'Backup & Recovery']) {
    expect(within(nav).getByRole('link', { name: label })).toBeVisible();
  }
  await user.click(group);
  expect(group).toHaveAttribute('aria-expanded', 'false');
  await user.keyboard('{Enter}');
  expect(group).toHaveAttribute('aria-expanded', 'true');
});

// Members can return to their own notice history without an administration section.
test('keeps personal notice history available from the user menu', async () => {
  const user = userEvent.setup();
  render(<MantineProvider><ShiftBoxShell session={{ role: 'member', mode: 'normal', installationState: 'active',
    permissions: ['notice.list'] }} alerts={[]}><h1>Workspaces</h1></ShiftBoxShell></MantineProvider>);
  const nav = screen.getByRole('navigation', { name: 'Main navigation' });
  expect(within(nav).queryByRole('button', { name: 'Administration' })).toBeNull();
  expect(within(nav).queryByRole('link', { name: 'Notices' })).toBeNull();
  await user.click(screen.getByRole('button', { name: 'User menu' }));
  expect(await screen.findByRole('menuitem', { name: 'Notice history' })).toHaveAttribute('href', '/notices/');
});

// Recovery review still exposes its one authorized destination under Administration.
test('groups recovery review access without normal workspace navigation', () => {
  render(<MantineProvider><ShiftBoxShell session={{ role: 'administrator', mode: 'recovery_review',
    installationState: 'recovery', permissions: ['recovery.review'] }} alerts={[]}>
    <h1>Recovery</h1>
  </ShiftBoxShell></MantineProvider>);
  const nav = screen.getByRole('navigation', { name: 'Main navigation' });
  expect(within(nav).getByRole('button', { name: 'Administration' })).toBeVisible();
  expect(within(nav).getByRole('link', { name: 'Backup & Recovery' })).toBeVisible();
  expect(within(nav).queryByRole('link', { name: 'Workspaces' })).toBeNull();
});
