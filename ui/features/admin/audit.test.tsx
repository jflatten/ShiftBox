import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { Audit } from './audit';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

const session = {
  user: { id: 'operator', role: 'administrator' }, mode: 'normal', installationState: 'active',
  permissions: ['audit.list'], csrfToken: 'fixture',
};

function json(payload: unknown, link?: string) {
  return { ok: true, status: 200, headers: new Headers(link ? { Link: link } : {}), json: async () => payload } as Response;
}

// Matching events can be paged without losing the requested action filter.
test('filters and pages through audit events', async () => {
  const path = '/api/v1/admin/audit';
  const event = { id: 'one', occurredAt: '2026-11-14T10:00:00Z', actorType: 'user', actorId: 'admin',
    action: 'user.approve', resourceType: 'user', resourceId: 'approved-account', outcome: 'allowed',
    reasonCode: 'membership_approved', metadata: { unrecognized: 'ignored fixture' } };
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input === path) return json([{ ...event, action: 'user.disable' }]);
    if (input === path + '?action=user.approve') return json([event],
      '</api/v1/admin/audit?action=user.approve&cursor=12>; rel="next"');
    if (input === path + '?action=user.approve&cursor=12') return json([{ ...event, id: 'two', resourceId: 'older-account' }]);
    throw new Error('unexpected request ' + input);
  });
  const user = userEvent.setup();
  render(<Providers><Audit /></Providers>);
  expect(await screen.findByRole('link', { name: 'Audit' })).toHaveAttribute('href', '/audit/');
  expect(await screen.findByText('user.disable')).toBeVisible();
  await user.type(screen.getByRole('textbox', { name: 'Action' }), 'user.approve');
  await user.click(screen.getByRole('button', { name: 'Apply filters' }));
  expect(await screen.findByText('approved-account')).toBeVisible();
  expect(screen.queryByText('ignored fixture')).toBeNull();
  await user.click(screen.getByRole('button', { name: 'Next page' }));
  expect(await screen.findByText('older-account')).toBeVisible();
  expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled();
  await user.click(screen.getByRole('button', { name: 'Previous page' }));
  expect(await screen.findByText('approved-account')).toBeVisible();
});

// A direct visit cannot fetch audit data without current administrator authority.
test('checks the audit permission before requesting history', async () => {
  const fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json({ ...session, permissions: [] });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  render(<Providers><Audit /></Providers>);
  expect(await screen.findByText('Administrator access required.')).toBeVisible();
  expect(fetch.mock.calls.map(([input]) => input)).toEqual(['/api/v1/session']);
});
