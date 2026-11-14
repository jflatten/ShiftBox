import { fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { Maintenance } from './maintenance';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

// An administrator supplies a release and an explicit local deadline. The
// accepted operation is presented using its server-defined steps.
test('schedules a campaign and presents its controller operation', async () => {
  const deadline = '2099-01-01T12:30';
  const campaign = { id: 'fixture', releaseId: 'release-two', deadline: new Date(deadline).toISOString(), state: 'scheduled', operationId: 'fixture' };
  let scheduled = false;
  const fetch = jest.fn<Promise<Response>, Parameters<typeof globalThis.fetch>>(async (input, init) => {
    if (input === '/api/v1/admin/maintenance' && init?.method === 'POST') {
      scheduled = true;
      return { ok: true, json: async () => ({ id: campaign.id }) } as Response;
    }
    if (input === '/api/v1/admin/maintenance') return { ok: true, json: async () => scheduled ? [campaign] : [] } as Response;
    if (input === '/api/v1/operations/fixture') return { ok: true, json: async () => ({
      id: campaign.id, status: 'succeeded', version: 2,
      steps: [{ key: 'update_guests', index: 0, label: 'Update retained workspaces', status: 'succeeded' }],
    }) } as Response;
    throw new Error('unexpected request');
  });
  globalThis.fetch = fetch;
  const user = userEvent.setup();
  render(<Providers><Maintenance csrfToken="fixture" /></Providers>);
  const submit = screen.getByRole('button', { name: 'Schedule maintenance' });
  await user.click(submit);
  const release = screen.getByLabelText(/target release/i);
  expect(release).toHaveFocus();
  await user.type(release, campaign.releaseId);
  fireEvent.change(screen.getByLabelText(/deadline/i), { target: { value: deadline } });
  await user.click(submit);
  expect(fetch).toHaveBeenCalledWith('/api/v1/admin/maintenance', expect.objectContaining({
    method: 'POST', body: JSON.stringify({ releaseId: campaign.releaseId, deadline: campaign.deadline }),
    headers: expect.objectContaining({ 'X-CSRF-Token': 'fixture', 'Idempotency-Key': expect.any(String) }),
  }));
  expect(await screen.findByRole('button', { name: 'Update retained workspaces: Completed' })).toBeVisible();
});
