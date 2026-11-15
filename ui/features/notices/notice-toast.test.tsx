import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { ShiftBoxShell } from '../../components/app-shell';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });
const session = { user: { id: 'member' }, role: 'member' as const, mode: 'normal', installationState: 'active',
  csrfToken: 'fixture', permissions: ['notice.list', 'notice.read', 'notice.dismiss'] };
const notices = [
  { id: 'one', title: 'Maintenance tonight', body: 'Save your work.', severity: 'warning' },
  { id: 'two', title: 'New templates', body: 'Explore the catalog.', severity: 'info' },
];
const json = (data: unknown) => ({ ok: true, status: 200, headers: new Headers(), json: async () => data }) as Response;

// Only one global message appears; dismissal saves both receipts before showing the next.
test('shows one app-wide toast and persists dismissal', async () => {
  let dismissed = false;
  const states: string[] = [];
  globalThis.fetch = jest.fn(async (_input, init) => {
    if (init?.method === 'PATCH') {
      expect(init.headers).toMatchObject({ 'X-CSRF-Token': 'fixture' });
      const state = JSON.parse(String(init.body)).state;
      states.push(state);
      if (state === 'dismissed') dismissed = true;
      return json(null);
    }
    return json(dismissed ? notices.slice(1) : notices);
  });
  const user = userEvent.setup();
  render(<Providers><ShiftBoxShell session={session} alerts={[]}><h1>Workspaces</h1></ShiftBoxShell></Providers>);
  expect(await screen.findByText('Maintenance tonight')).toBeVisible();
  expect(screen.queryByText('New templates')).toBeNull();
  expect(screen.getByRole('link', { name: 'View notices' })).toHaveAttribute('href', '/notices/');
  await user.click(screen.getByRole('button', { name: 'Dismiss Maintenance tonight' }));
  expect(await screen.findByText('New templates')).toBeVisible();
  expect(states).toEqual(['read', 'dismissed']);
});

// A failed receipt must retain the notice and allow an explicit retry, not hide it locally.
test('keeps the toast when saving dismissal fails', async () => {
  globalThis.fetch = jest.fn(async (_input, init) => {
    if (init?.method === 'PATCH') throw new Error('offline');
    return json(notices);
  });
  const user = userEvent.setup();
  render(<Providers><ShiftBoxShell session={session} alerts={[]}><h1>Workspaces</h1></ShiftBoxShell></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Dismiss Maintenance tonight' }));
  await waitFor(() => expect(screen.getByRole('button', { name: 'Dismiss Maintenance tonight' })).toBeEnabled());
  expect(screen.getByText('Maintenance tonight')).toBeVisible();
  expect(screen.queryByText('New templates')).toBeNull();
  expect(screen.getByText('Unable to complete the request')).toBeVisible();
});

// Limited sessions must never fetch or show normal-operation notices.
test('does not request notices during mandatory password change', () => {
  globalThis.fetch = jest.fn();
  render(<Providers><ShiftBoxShell session={{ ...session, mode: 'password_change' }} alerts={[]}><h1>Settings</h1></ShiftBoxShell></Providers>);
  expect(globalThis.fetch).not.toHaveBeenCalled();
});

// Global announcements float outside the main flow so they cannot push workspace controls down.
test('renders the notice in a fixed overlay outside main content', async () => {
  globalThis.fetch = jest.fn(async () => json(notices));
  render(<Providers><ShiftBoxShell session={session} alerts={[]}><h1>Workspaces</h1></ShiftBoxShell></Providers>);
  const toast = await screen.findByRole('region', { name: 'Application notice' });
  expect(toast).toHaveStyle({ position: 'fixed' });
  expect(screen.getByRole('main')).not.toContainElement(toast);
  expect(screen.getByRole('link', { name: 'View notices' })).toBeVisible();
});
