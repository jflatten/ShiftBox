import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { focusManager } from '@tanstack/react-query';
import { Providers } from '../../app/providers';
import { Notices } from './notices';

const originalFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = originalFetch;
  focusManager.setFocused(undefined);
});

const session = {
  user: { id: 'member', role: 'member' }, mode: 'normal', installationState: 'active',
  permissions: ['notice.list', 'notice.read', 'notice.dismiss'], csrfToken: 'fixture',
};
const notice = { id: 'notice-one', severity: 'warning', title: 'Scheduled maintenance', body: 'Save your work.' };

function json(payload: unknown) {
  return { ok: true, status: 200, headers: new Headers(), json: async () => payload } as Response;
}

// One dismissal records the required read receipt and remains available in personal history.
test('reads, dismisses and reopens a notice from history', async () => {
  let readAt: string | null = null;
  let dismissedAt: string | null = null;
  globalThis.fetch = jest.fn(async (input, init) => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/notices') return json(dismissedAt ? [] : [{ ...notice, readAt, dismissedAt }]);
    if (input === '/api/v1/notices?includeDismissed=true') return json([{ ...notice, readAt, dismissedAt }]);
    if (input === '/api/v1/notices/notice-one/receipt' && init?.method === 'PATCH') {
      expect(init.headers).toMatchObject({ 'X-CSRF-Token': 'fixture' });
      if (JSON.parse(String(init.body)).state === 'read') readAt = '2026-11-14T10:00:00Z';
      else dismissedAt = '2026-11-14T10:01:00Z';
      return json(null);
    }
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><Notices /></Providers>);
  expect(await screen.findByRole('link', { name: 'Notices' })).toHaveAttribute('href', '/notices/');
  expect(await screen.findByText(notice.title)).toBeVisible();
  expect(globalThis.fetch).not.toHaveBeenCalledWith('/api/v1/admin/infrastructure', expect.anything());
  expect(screen.queryByRole('button', { name: 'Publish notice' })).toBeNull();
  await user.click(screen.getByRole('button', { name: `Dismiss ${notice.title}` }));
  expect(await screen.findByText('No active notices')).toBeVisible();
  await user.click(screen.getByRole('radio', { name: 'All notices' }));
  expect(await screen.findByText(notice.title)).toBeVisible();
  expect(screen.getByText('Dismissed')).toBeVisible();
});

// The certificate warning survives dismissal of a personal notice and clears after replacement.
test('shows active certificate warnings to administrators', async () => {
  let dismissed = false;
  let replaced = false;
  const detail = 'Replace the browser certificate before 2026-12-01 15:04 UTC.';
  globalThis.fetch = jest.fn(async (input, init) => {
    if (input === '/api/v1/session') return json({ ...session,
      user: { id: 'admin', role: 'administrator' }, permissions: [...session.permissions, 'infrastructure.view'] });
    if (input === '/api/v1/admin/infrastructure') return json({ conditions: replaced ? [] : [
      { key: 'browser_certificate.expiring', severity: 'warning', active: true, detail },
    ] });
    if (input === '/api/v1/notices') return json(dismissed ? [] : [{ ...notice, readAt: '2026-11-14T10:00:00Z' }]);
    if (input === '/api/v1/notices/notice-one/receipt' && init?.method === 'PATCH') {
      dismissed = true;
      return json(null);
    }
    throw new Error('unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><Notices /></Providers>);
  const conditions = within(screen.getByRole('region', { name: 'Persistent conditions' }));
  expect(await conditions.findByText('Browser certificate expires soon')).toBeVisible();
  expect(conditions.getByText(detail)).toBeVisible();
  expect(conditions.queryByRole('button')).toBeNull();
  await user.click(await screen.findByRole('button', { name: `Dismiss ${notice.title}` }));
  expect(await screen.findByText('No active notices')).toBeVisible();
  expect(conditions.getByText(detail)).toBeVisible();

  replaced = true;
  act(() => focusManager.setFocused(false));
  act(() => focusManager.setFocused(true));
  await waitFor(() => expect(conditions.queryByText(detail)).toBeNull());
});

// Administrators publish with the current CSRF token and see the saved notice.
test('publishes a systemwide notice', async () => {
  let published = false;
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/session') return json({ ...session,
      user: { id: 'admin', role: 'administrator' }, permissions: [...session.permissions, 'notice.publish'] });
    if (input === '/api/v1/notices') return json(published ? [notice] : []);
    if (input === '/api/v1/admin/notices' && init?.method === 'POST') {
      published = true;
      return json(notice);
    }
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Notices /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Publish notice' }));
  await user.type(await screen.findByRole('textbox', { name: 'Title' }), notice.title);
  await user.type(screen.getByRole('textbox', { name: 'Message' }), notice.body);
  await user.selectOptions(screen.getByRole('combobox', { name: 'Severity' }), 'warning');
  await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Publish notice' }));
  expect(await screen.findByText(notice.title)).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/notices', expect.objectContaining({
    method: 'POST', body: JSON.stringify({ severity: 'warning', title: notice.title, body: notice.body }),
    headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture' }),
  }));
});

// An already-expired announcement must be corrected before publishing to everyone.
test('rejects an expiry before an immediate start', async () => {
  globalThis.fetch = jest.fn(async input => input === '/api/v1/session' ? json({ ...session,
    user: { id: 'admin', role: 'administrator' }, permissions: [...session.permissions, 'notice.publish'] }) : json([]));
  const user = userEvent.setup();
  render(<Providers><Notices /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Publish notice' }));
  await user.type(await screen.findByRole('textbox', { name: 'Title' }), 'Maintenance');
  await user.type(screen.getByRole('textbox', { name: 'Message' }), 'Save your work.');
  fireEvent.change(screen.getByLabelText('Expiry time (optional)'), { target: { value: '2020-01-01T12:00' } });
  await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Publish notice' }));
  expect(await screen.findByText('Choose an expiry after the start time.')).toBeVisible();
  expect(globalThis.fetch).not.toHaveBeenCalledWith('/api/v1/admin/notices', expect.anything());
});

// Cursor navigation must use the server link; changing the view returns to the first page.
test('pages through notices and resets pagination for history', async () => {
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === '/api/v1/notices') return { ...json([notice]), headers: new Headers({ Link: '</api/v1/notices?cursor=next-page>; rel="next"' }) };
    if (input === '/api/v1/notices?cursor=next-page') return json([{ ...notice, id: 'second', title: 'Second notice' }]);
    if (input === '/api/v1/notices?includeDismissed=true') return json([{ ...notice, dismissedAt: '2026-11-14T10:00:00Z' }]);
    throw new Error('Unexpected request');
  });
  const user = userEvent.setup();
  render(<Providers><Notices /></Providers>);
  await user.click(await screen.findByRole('button', { name: 'Next page' }));
  expect(await screen.findByText('Second notice')).toBeVisible();
  await user.click(screen.getByRole('radio', { name: 'All notices' }));
  expect(await screen.findByText('Dismissed')).toBeVisible();
  expect(screen.queryByText('Second notice')).toBeNull();
});
