import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { People } from './people';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

const session = {
  user: { id: 'operator', role: 'administrator' }, mode: 'normal', installationState: 'active',
  permissions: ['user.list'], csrfToken: 'fixture',
};

function json(payload: unknown) {
  return { ok: true, status: 200, json: async () => payload } as Response;
}

// A failed membership read can be retried and then shows the distinct
// account states and credential modes returned by the controller.
test('retries a failed membership read and shows account states', async () => {
  let reads = 0;
  globalThis.fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json(session);
    if (input !== '/api/v1/admin/users') throw new Error('unexpected request');
    reads++;
    if (reads === 1) return { ok: false, status: 503, headers: new Headers() } as Response;
    return json([
      { id: 'owner', primaryEmail: 'admin@example.test', membershipState: 'active', credentialMode: 'local', role: 'administrator' },
      { id: 'pending', primaryEmail: 'pending@example.test', membershipState: 'pending', credentialMode: 'external', role: 'member' },
      { id: 'disabled', primaryEmail: 'disabled@example.test', membershipState: 'disabled', credentialMode: 'local', role: 'member' },
    ]);
  });
  const user = userEvent.setup();
  render(<Providers><People /></Providers>);
  expect(await screen.findByRole('alert')).toHaveTextContent('Unable to complete the request');
  await user.click(screen.getByRole('button', { name: 'Retry' }));
  const active = await screen.findByRole('row', { name: /admin@example.test/ });
  const pending = screen.getByRole('row', { name: /pending@example.test/ });
  const disabled = screen.getByRole('row', { name: /disabled@example.test/ });
  expect(within(active).getByText('Active')).toBeVisible();
  expect(within(active).getByText('Local')).toBeVisible();
  expect(within(pending).getByText('Pending')).toBeVisible();
  expect(within(pending).getByText('External')).toBeVisible();
  expect(within(disabled).getByText('Disabled')).toBeVisible();
  expect(reads).toBe(2);
});

// Direct navigation by a member never fetches the administrator's list.
test('requires administrator access before reading memberships', async () => {
  const fetch = jest.fn(async input => {
    if (input === '/api/v1/session') return json({ ...session, user: { id: 'member', role: 'member' }, permissions: [] });
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  render(<Providers><People /></Providers>);
  expect(await screen.findByText('Administrator access required.')).toBeVisible();
  expect(fetch.mock.calls.map(([input]) => input)).toEqual(['/api/v1/session']);
});
