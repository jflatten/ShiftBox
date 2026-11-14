import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { Notices } from './notices';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

const session = {
  user: { id: 'member', role: 'member' }, mode: 'normal', installationState: 'active',
  permissions: ['notice.list', 'notice.read', 'notice.dismiss'], csrfToken: 'fixture',
};
const notice = { id: 'notice-one', severity: 'warning', title: 'Scheduled maintenance', body: 'Save your work.' };

function json(payload: unknown) {
  return { ok: true, status: 200, headers: new Headers(), json: async () => payload } as Response;
}

// Members read before dismissing and can retrieve their dismissed history.
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
  expect(screen.queryByRole('button', { name: 'Publish notice' })).toBeNull();
  expect(screen.queryByRole('button', { name: 'Dismiss' })).toBeNull();
  await user.click(screen.getByRole('button', { name: 'Mark as read' }));
  await user.click(await screen.findByRole('button', { name: 'Dismiss' }));
  expect(await screen.findByText('No notices.')).toBeVisible();
  await user.click(screen.getByRole('checkbox', { name: 'Include dismissed and historical notices' }));
  expect(await screen.findByText(notice.title)).toBeVisible();
  expect(screen.getByText('Dismissed')).toBeVisible();
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
  await user.type(await screen.findByRole('textbox', { name: 'Title' }), notice.title);
  await user.type(screen.getByRole('textbox', { name: 'Message' }), notice.body);
  await user.selectOptions(screen.getByRole('combobox', { name: 'Severity' }), 'warning');
  await user.click(screen.getByRole('button', { name: 'Publish notice' }));
  expect(await screen.findByText(notice.title)).toBeVisible();
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/notices', expect.objectContaining({
    method: 'POST', body: JSON.stringify({ severity: 'warning', title: notice.title, body: notice.body }),
    headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture' }),
  }));
});
