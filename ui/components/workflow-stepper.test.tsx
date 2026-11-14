import { MantineProvider } from '@mantine/core';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { WorkflowStepper, type WorkflowOperation } from './workflow-stepper';

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
