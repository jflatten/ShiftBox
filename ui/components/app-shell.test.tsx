import { MantineProvider } from '@mantine/core';
import { act, render, screen } from '@testing-library/react';
import { ShiftBoxShell } from './app-shell';
import userEvent from '@testing-library/user-event';
import { Providers } from '../app/providers';


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
  expect(screen.queryByRole('link', { name: 'People' })).not.toBeInTheDocument();
});

// The approved People navigation label points to the membership route.
test('links authorized administrators to People', () => {
  render(<MantineProvider><ShiftBoxShell session={{ role: 'administrator', mode: 'normal', installationState: 'active', permissions: ['user.list'] }} alerts={[]}><h1>Overview</h1></ShiftBoxShell></MantineProvider>);
  expect(screen.getByRole('link', { name: 'People' })).toHaveAttribute('href', '/users/');
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
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(fetch).toHaveBeenCalledWith('/api/v1/session', expect.objectContaining({
      method: 'DELETE', headers: { 'X-CSRF-Token': 'fixture' },
    }));
    expect(await screen.findByRole('link', { name: 'Sign in' })).toBeVisible();
    expect(screen.queryByRole('heading', { name: 'Change password' })).not.toBeInTheDocument();
  } finally { globalThis.fetch = original; }
});
