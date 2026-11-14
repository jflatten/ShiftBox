import { render, screen } from '@testing-library/react';
import Home from './page';
import { Providers } from './providers';

// An unavailable controller must remain an explicit condition, not an empty successful workspace list.
test('shows a persistent connection error when the session API is unavailable', async () => {
  const original = globalThis.fetch;
  globalThis.fetch = jest.fn().mockResolvedValue({ ok: false, status: 503 });
  try {
    render(<Providers><Home /></Providers>);
    expect(await screen.findByRole('alert')).toHaveTextContent('Unable to connect to ShiftBox');
    expect(screen.queryByRole('link', { name: 'Workspaces' })).not.toBeInTheDocument();
  } finally { globalThis.fetch = original; }
});
