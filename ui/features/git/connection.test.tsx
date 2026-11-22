import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { GitConnection } from './connection';

const headers = new Headers();

// Connecting and disconnecting are explicit actions and both carry the
// current CSRF token. The screen refreshes server state after disconnection.
test('connects GitHub and confirms before disconnecting', async () => {
  const original = globalThis.fetch;
  let state = 'reconnect_required';
  const fetch = jest.fn(async (_path: string, init?: RequestInit) => {
    if (init?.method === 'POST') return { ok: true, json: async () => ({
      authorizationUrl: 'https://github.com/login/oauth/authorize?state=fixture', expiresAt: '2040-01-01T12:00:00Z',
    }) };
    if (init?.method === 'DELETE') {
      state = 'revoked';
      return { ok: true, status: 204 };
    }
    return { ok: true, headers, json: async () => ({ state }) };
  });
  globalThis.fetch = fetch as unknown as typeof globalThis.fetch;
  try {
    const user = userEvent.setup();
    render(<Providers><GitConnection userID="user" csrfToken="fixture" /></Providers>);
    await user.click(await screen.findByRole('button', { name: 'Reconnect GitHub' }));
    expect(await screen.findByRole('link', { name: 'Continue to GitHub' })).toHaveAttribute('href', 'https://github.com/login/oauth/authorize?state=fixture');
    await user.click(screen.getByRole('button', { name: 'Disconnect GitHub' }));
    expect(fetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false);
    await user.click(screen.getByRole('button', { name: 'Confirm disconnect' }));
    expect(await screen.findByText('GitHub is disconnected.')).toBeVisible();
    for (const method of ['POST', 'DELETE']) {
      expect(fetch).toHaveBeenCalledWith('/api/v1/git/authorization', expect.objectContaining({
        method, headers: { 'X-CSRF-Token': 'fixture' },
      }));
    }
  } finally { globalThis.fetch = original; }
});
