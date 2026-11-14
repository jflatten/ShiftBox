import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { Templates } from './templates';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

// Templates shows only the current server page and follows the cursor when
// a user's permitted templates span more than one page.
test('lists current template revisions and follows the server cursor', async () => {
  globalThis.fetch = jest.fn(async input => {
    const responses: Record<string, unknown> = {
      '/api/v1/session': { user: { id: 'member', role: 'member' }, mode: 'normal', installationState: 'active', permissions: [], csrfToken: 'fixture' },
      '/api/v1/templates': { items: [{ id: 'one', name: 'Example project', currentRevision: 2 }], nextCursor: 'after-one' },
      '/api/v1/templates?cursor=after-one': { items: [{ id: 'two', name: 'Second project', currentRevision: 1 }], nextCursor: null },
    };
    if (typeof input !== 'string' || !(input in responses)) throw new Error('unexpected request');
    return { ok: true, status: 200, json: async () => responses[input] } as Response;
  });
  const user = userEvent.setup();
  render(<Providers><Templates /></Providers>);
  expect(await screen.findByRole('cell', { name: 'Example project' })).toBeVisible();
  expect(screen.getByRole('cell', { name: '2' })).toBeVisible();
  await user.click(screen.getByRole('button', { name: 'Next page' }));
  expect(await screen.findByRole('cell', { name: 'Second project' })).toBeVisible();
  expect(screen.queryByRole('cell', { name: 'Example project' })).toBeNull();
});
