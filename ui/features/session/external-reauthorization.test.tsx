import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { ExternalReauthorization } from './external-reauthorization';

const dueAt = '2040-01-01T12:00:00Z';

// A failed request keeps renewal available. Only a fresh same-origin Dex
// response opens an authorization link.
test('retries renewal and offers the controller authorization URL', async () => {
  const original = globalThis.fetch;
  const fetch = jest.fn()
    .mockResolvedValueOnce({ ok: false, status: 503, headers: new Headers() })
    .mockResolvedValueOnce({ ok: true, json: async () => ({
      authorizationUrl: 'https://foreign.test/dex/auth?state=fixture', expiresAt: dueAt,
    }) })
    .mockResolvedValueOnce({ ok: true, json: async () => ({
      authorizationUrl: window.location.origin + '/dex/auth?state=fixture', expiresAt: dueAt,
    }) });
  globalThis.fetch = fetch;
  try {
    const user = userEvent.setup();
    render(<Providers><ExternalReauthorization csrfToken="fixture" dueAt={dueAt} expired={false} /></Providers>);
    expect(screen.getByText(/2040/)).toBeVisible();
    const renew = screen.getByRole('button', { name: 'Renew authorization' });
    await user.click(renew);
    expect(await screen.findByText('Unable to complete the request')).toBeVisible();
    await user.click(renew);
    expect(await screen.findByText('The authorization link is unavailable. Try again.')).toBeVisible();
    expect(screen.queryByRole('link', { name: 'Continue to organization' })).not.toBeInTheDocument();
    await user.click(renew);
    expect(await screen.findByRole('link', { name: 'Continue to organization' })).toHaveAttribute('href', '/dex/auth?state=fixture');
    expect(fetch).toHaveBeenCalledTimes(3);
    expect(fetch).toHaveBeenLastCalledWith('/api/v1/session/external-reauthorization', expect.objectContaining({
      method: 'POST', headers: { 'X-CSRF-Token': 'fixture' },
    }));
  } finally { globalThis.fetch = original; }
});
