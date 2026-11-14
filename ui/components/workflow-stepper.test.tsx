import { MantineProvider } from '@mantine/core';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { WorkflowStepper, type WorkflowOperation } from './workflow-stepper';
import { useOperation } from '../lib/use-operation';

const operation: WorkflowOperation = {
  id: 'operation-one', status: 'running', currentStep: 'boot',
  steps: [
    { key: 'create', index: 0, label: 'Create machine', status: 'succeeded', detail: 'Machine created.' },
    { key: 'boot', index: 1, label: 'Start machine', status: 'active', detail: 'Waiting for the guest.' },
    { key: 'ide', index: 2, label: 'Open IDE', status: 'pending' },
  ],
};

function view(value: WorkflowOperation) {
  return <MantineProvider><WorkflowStepper operation={value} /></MantineProvider>;
}

// Completed-step inspection must not advance the server's workflow or unlock future steps.
test('keeps authoritative progress when inspecting a completed step', async () => {
  const user = userEvent.setup();
  render(view(operation));
  expect(screen.getByRole('button', { name: 'Open IDE: Pending' })).toBeDisabled();
  expect(screen.getByRole('button', { name: 'Start machine: In progress' })).toHaveAttribute('aria-current', 'step');
  await user.click(screen.getByRole('button', { name: 'Create machine: Completed' }));
  expect(screen.getByText('Machine created.')).toBeVisible();
  expect(screen.getByText('Waiting for the guest.')).toBeVisible();
  expect(screen.getByRole('button', { name: 'Start machine: In progress' })).toHaveAttribute('aria-current', 'step');
  expect(screen.getByRole('status')).toHaveTextContent('Start machine');
});

// A later server snapshot must update the live status while retaining earlier failure evidence.
test('renders durable failure history and server updates', () => {
  const failed: WorkflowOperation = { ...operation, status: 'failed', steps: operation.steps.map(step => step.key === 'boot' ? { ...step, status: 'failed', problem: { title: 'Guest did not respond', detail: 'Check the machine and retry.' } } : step) };
  const { rerender } = render(view(failed));
  expect(screen.getByRole('alert')).toHaveTextContent('Guest did not respond');
  expect(screen.getByRole('alert')).toHaveTextContent('Check the machine and retry.');
  rerender(view({ ...failed, status: 'waiting' }));
  expect(screen.getByRole('status')).toHaveTextContent('Waiting');
  expect(screen.getByRole('alert')).toHaveTextContent('Guest did not respond');
});

// Completion comes from durable state, and skipped work must never be described as completed work.
test('distinguishes skipped steps from successful completion', () => {
  render(view({ ...operation, status: 'succeeded', currentStep: null, steps: operation.steps.map(step => ({ ...step, status: step.key === 'boot' ? 'skipped' : 'succeeded' })) }));
  expect(screen.getByRole('status')).toHaveTextContent('Completed');
  expect(screen.getByRole('button', { name: 'Start machine: Skipped' })).toBeEnabled();
  expect(screen.queryByRole('button', { current: 'step' })).not.toBeInTheDocument();
});

// A missing event must refetch and render current server progress, retaining failure details and future-step locks.
test('recovers an SSE gap through the query hook into the visible Stepper', async () => {
  const originalFetch = globalThis.fetch;
  const originalSource = Object.getOwnPropertyDescriptor(globalThis, 'EventSource');
  const listeners = new Map<string, EventListener>();
  const close = jest.fn();
  class Source {
    addEventListener(type: string, listener: EventListener) { listeners.set(type, listener); }
    removeEventListener(type: string) { listeners.delete(type); }
    close = close;
  }
  const problem = { title: 'An earlier start attempt failed', detail: 'Retry is waiting for the guest.' };
  const initial = { ...operation, version: 1, steps: operation.steps.map(step => step.key === 'boot' ? { ...step, problem } : step) };
  let snapshot = initial;
  const fetcher = jest.fn(async () => ({ ok: true, json: async () => snapshot }));
  globalThis.fetch = fetcher as unknown as typeof fetch;
  Object.defineProperty(globalThis, 'EventSource', { configurable: true, value: Source });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  function Progress() {
    const query = useOperation(operation.id);
    return query.data ? <WorkflowStepper operation={query.data} /> : null;
  }
  const mounted = render(<MantineProvider><QueryClientProvider client={client}><Progress /></QueryClientProvider></MantineProvider>);
  try {
    expect(await screen.findByRole('status')).toHaveTextContent('Start machine');
    await waitFor(() => expect(listeners.has('operation.updated')).toBe(true));
    act(() => listeners.get('operation.updated')!(new MessageEvent('operation.updated', { lastEventId: '1' })));
    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
    snapshot = { ...initial, version: 3, status: 'waiting' };
    // Event 2 was lost; the event payload itself is deliberately not a usable snapshot.
    act(() => listeners.get('operation.updated')!(new MessageEvent('operation.updated', { lastEventId: '3', data: '{}' })));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Waiting'));
    expect(fetcher).toHaveBeenCalledTimes(3);
    expect(screen.getByRole('alert')).toHaveTextContent('An earlier start attempt failed');
    expect(screen.getByRole('button', { name: 'Open IDE: Pending' })).toBeDisabled();
    expect(screen.getByRole('status')).toHaveAttribute('aria-live', 'polite');
  } finally {
    mounted.unmount();
    client.clear();
    globalThis.fetch = originalFetch;
    if (originalSource) Object.defineProperty(globalThis, 'EventSource', originalSource);
    else Reflect.deleteProperty(globalThis, 'EventSource');
  }
  expect(close).toHaveBeenCalledTimes(1);
});
