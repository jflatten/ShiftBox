import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../providers';
import { LocalHelper } from './helper';

const request = {
  state: 's'.repeat(32),
  challenge: 'A'.repeat(43),
  redirectUri: 'http://127.0.0.1:43210/callback',
};
const query = new URLSearchParams(request).toString();
const callback = request.redirectUri + '?' + new URLSearchParams({ code: 'A'.repeat(43), state: request.state });
const current = {
  user: { id: 'account', role: 'member', displayName: 'Alex Developer', primaryEmail: 'alex@example.test' },
  mode: 'normal', installationState: 'active', permissions: ['workspace.list'], csrfToken: 'fixture-csrf',
  expiresAt: '2029-01-01T00:00:00Z',
};

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

function renderHelper(search = '', onReturn = jest.fn()) {
  render(<Providers><LocalHelper query={search} onReturn={onReturn} /></Providers>);
  return onReturn;
}

function json(body: unknown, status = 200) {
  return { ok: status < 400, status, json: async () => body, headers: { get: () => "application/json" } } as unknown as Response;
}

// The installer links to the exact authenticated macOS archive, without implying launch.
test('offers the internal macOS artifact to the signed-in account', async () => {
  globalThis.fetch = jest.fn().mockResolvedValue(json(current));
  renderHelper();
  expect(await screen.findByRole('heading', { name: 'Install Local Helper' })).toBeVisible();
  const download = await screen.findByRole('link', { name: 'Download for macOS' });
  expect(download).toHaveAttribute('href', '/api/v1/ssh/helper/download');
  expect(screen.getByText(/internal build/i)).toBeVisible();
});

// Pairing presents the account and installation before the keyboard submits the bound request.
test('approves the exact loopback request with the signed-in account and redirects to the local helper', async () => {
  globalThis.fetch = jest.fn(async (url) => url === '/api/v1/session' ? json(current) : json({ callbackUrl: callback }));
  const redirect = renderHelper(query);
  const details = await screen.findByRole('region', { name: 'Pairing details' });
  expect(within(details).getByText('alex@example.test')).toBeVisible();
  expect(within(details).getByText(window.location.origin)).toBeVisible();

  const approve = screen.getByRole('button', { name: 'Approve pairing' });
  approve.focus();
  await userEvent.setup().keyboard('{Enter}');
  expect(fetch).toHaveBeenCalledWith('/api/v1/ssh/helper/approve', expect.objectContaining({
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'fixture-csrf' },
    body: JSON.stringify(request),
  }));
  expect(await screen.findByText('Approval sent')).toBeVisible();
  expect(redirect).toHaveBeenCalledWith(callback);
});

// A substituted callback leaves the browser on ShiftBox with an actionable error.
test('refuses a returned callback at a different local port', async () => {
  globalThis.fetch = jest.fn(async (url) => url === '/api/v1/session' ? json(current) :
    json({ callbackUrl: callback.replace('43210', '43211') }));
  const redirect = renderHelper(query);
  await userEvent.setup().click(await screen.findByRole('button', { name: 'Approve pairing' }));
  expect(await screen.findByText('Restart pairing from the macOS helper.')).toBeVisible();
  expect(redirect).not.toHaveBeenCalled();
});

// An incomplete or externally directed request cannot surface an approval action.
test('rejects a non-loopback request before approval', async () => {
  globalThis.fetch = jest.fn().mockResolvedValue(json(current));
  renderHelper(new URLSearchParams({ ...request, redirectUri: 'https://external.example.test/callback' }).toString());
  expect(await screen.findByText('Restart pairing from the macOS helper.')).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Approve pairing' })).not.toBeInTheDocument();
});
