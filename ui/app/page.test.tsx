import { render, screen } from '@testing-library/react';
import Home from './page';
import { Providers } from './providers';

// An unavailable controller must remain an explicit condition, not an empty successful workspace list.
test('shows a persistent connection error when the session API is unavailable', async () => {
  const original = globalThis.fetch;
  globalThis.fetch = jest.fn().mockResolvedValue({ ok: false, status: 503, headers: new Headers() });
  try {
    render(<Providers><Home /></Providers>);
    expect(await screen.findByRole('alert')).toHaveTextContent('Unable to complete the request');
    expect(screen.queryByRole('link', { name: 'Workspaces' })).not.toBeInTheDocument();
  } finally { globalThis.fetch = original; }
});

// Public controller guidance and request references survive the session-read boundary.
test('shows the controller problem from a failed session read', async () => {
  const original = globalThis.fetch;
  globalThis.fetch = jest.fn().mockResolvedValue({ ok: false, status: 503, headers: new Headers({ 'Content-Type': 'application/problem+json' }), json: async () => ({ type: 'about:blank', title: 'Installation needs attention', status: 503, code: 'installation.blocked', requestId: 'session-reference', detail: 'Contact your administrator.' }) });
  try {
    render(<Providers><Home /></Providers>);
    expect(await screen.findByRole('alert')).toHaveTextContent('Installation needs attention');
    expect(screen.getByRole('alert')).toHaveTextContent('session-reference');
  } finally { globalThis.fetch = original; }
});

// The sign-in choices let organizational users start the upstream flow.
test('offers local and organizational sign-in', async () => {
  const original = globalThis.fetch;
  globalThis.fetch = jest.fn().mockResolvedValue({ ok: false, status: 401 });
  try {
    render(<Providers><Home /></Providers>);
    expect(await screen.findByRole('link', { name: 'Sign in' })).toHaveAttribute('href', '/auth/login');
    expect(screen.getByRole('link', { name: 'Sign in with your organization' })).toHaveAttribute('href', '/auth/external');
  } finally { globalThis.fetch = original; }
});
