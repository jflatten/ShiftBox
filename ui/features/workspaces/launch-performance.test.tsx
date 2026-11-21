import { MantineProvider } from '@mantine/core';
import { render, screen, within } from '@testing-library/react';
import { LaunchPerformance } from './launch-performance';

// Later hook failure must not hide verified readiness or present a successful total setup time.
test('shows verified readiness beside a failed hook and durable source details', () => {
  render(<MantineProvider><LaunchPerformance performance={{
    timeToIdeReadyMs: 12500,
    steps: [{ key: 'finish_setup', label: 'Finish remaining setup hooks', status: 'failed', durationMs: 3000 }],
    events: [{ id: 'guest-timing', source: 'guest', phase: 'ide_install', occurredAt: '2026-11-20T12:00:10Z', outcome: 'succeeded', durationMs: 5200 }],
    cache: { requests: 3, cachedRequests: 2, failedRequests: 1, rateLimitedRequests: 1, cachedBytes: 2097152,
      upstreamBytes: 1048576, windowMs: 4000 },
  }} /></MantineProvider>);
  const panel = screen.getByRole('region', { name: 'Launch performance' });
  expect(within(panel).getByText('Time to IDE ready')).toBeVisible();
  expect(within(panel).getByText('12.5 s')).toBeVisible();
  expect(within(panel).queryByText('Time to setup complete')).not.toBeInTheDocument();
  expect(within(panel).getByText('Finish remaining setup hooks')).toBeVisible();
  expect(within(panel).getByText('Install browser IDE')).toBeVisible();
  expect(within(panel).getByText('2 MiB')).toBeVisible();
  expect(within(panel).queryByText('guest-timing')).not.toBeInTheDocument();
});

// Accounting follows controller order, while guest durations remain grouped in the recorded measurements.
test('preserves controller order and groups guest measurements by step', () => {
  const occurredAt = '2026-11-20T12:00:10Z';
  render(<MantineProvider><LaunchPerformance performance={{
    steps: [
      { key: 'provision', label: 'Provision workspace', status: 'succeeded', durationMs: 5000, startedAt: '2026-11-20T12:00:08Z' },
      { key: 'enroll', label: 'Enroll the guest', status: 'succeeded', durationMs: 10000 },
      { key: 'configure', label: 'Configure the development container', status: 'succeeded', durationMs: 19000 },
      { key: 'verify_ide', label: 'Verify the browser IDE', status: 'succeeded', durationMs: 300 },
      { key: 'finish_setup', label: 'Finish remaining setup hooks', status: 'active', durationMs: 1000 },
    ],
    events: [
      { id: '1', source: 'provider', phase: 'ec2_run_instances', outcome: 'succeeded', durationMs: 1000, occurredAt },
      { id: '2', source: 'controller', phase: 'guest_authenticated', outcome: 'observed', durationMs: 0, occurredAt },
      { id: '3', source: 'provider', phase: 'vm_running', outcome: 'observed', durationMs: 0, occurredAt },
      { id: '4', source: 'guest', phase: 'remaining_hooks', outcome: 'succeeded', durationMs: 200, occurredAt },
      { id: '5', source: 'guest', phase: 'ide_start', outcome: 'succeeded', durationMs: 300, occurredAt },
    ],
  }} /></MantineProvider>);

  const table = screen.getByRole('table', { name: 'Launch timing details' });
  const groups = within(table).getAllByRole('rowgroup').slice(1);
  expect(groups.map(group => group.getAttribute('aria-label'))).toEqual([
    'Provision workspace', 'Enroll the guest', 'Configure the development container',
    'Verify the browser IDE', 'Finish remaining setup hooks',
  ]);
  expect(within(groups[0]).getByRole('rowheader', { name: 'Provision workspace' })).toHaveAttribute('scope', 'rowgroup');
  expect(within(groups[0]).getByRole('rowheader', { name: 'Submit EC2 launch' })).toBeVisible();
  expect(within(groups[0]).getByRole('rowheader', { name: 'Provider confirmed VM running' })).toBeVisible();
  expect(within(groups[1]).getByRole('rowheader', { name: 'First authenticated guest poll' })).toBeVisible();
  expect(within(table).queryByText('Start browser IDE')).not.toBeInTheDocument();
  expect(within(table).queryByText('Run remaining hooks')).not.toBeInTheDocument();
  const diagnostics = within(screen.getByRole('table', { name: 'Additional timing measurements' }));
  expect(within(diagnostics.getByRole('rowgroup', { name: 'Configure the development container' })).getByText('Start browser IDE')).toBeVisible();
  expect(within(diagnostics.getByRole('rowgroup', { name: 'Finish remaining setup hooks' })).getByText('Run remaining hooks')).toBeVisible();
});

// Separate provider boot belongs to enrollment, and older launches can have a partial step list.
test('groups a separate VM boot and preserves events with no matching step', () => {
  const occurredAt = '2026-11-20T12:00:10Z';
  render(<MantineProvider><LaunchPerformance performance={{
    steps: [
      { key: 'provision', label: 'Provision workspace', status: 'succeeded', durationMs: 10000 },
      { key: 'enroll', label: 'Enroll the guest', status: 'failed', durationMs: 20000, startedAt: '2026-11-20T12:00:09Z' },
    ],
    events: [
      { id: '1', source: 'provider', phase: 'enrollment_delivery', outcome: 'succeeded', durationMs: 400, occurredAt },
      { id: '2', source: 'provider', phase: 'vm_start', outcome: 'failed', durationMs: 300, reasonCode: 'timed_out', occurredAt: '2026-11-20T12:00:10.500Z' },
      { id: '3', source: 'provider', phase: 'vm_running', outcome: 'observed', durationMs: 0, occurredAt },
      { id: '4', source: 'guest', phase: 'ide_start', outcome: 'succeeded', durationMs: 300, occurredAt },
    ],
  }} /></MantineProvider>);

  const enroll = screen.getByRole('rowgroup', { name: 'Enroll the guest' });
  expect(within(enroll).getAllByRole('rowheader').map(cell => cell.textContent)).toEqual([
    'Enroll the guest', 'Deliver enrollment media', 'Start workspace VM', 'Provider confirmed VM running', 'Other time in step',
  ]);
  expect(within(enroll).getByText('Timed out')).toBeVisible();
  expect(within(screen.getByRole('rowgroup', { name: 'Other launch timings' })).getByText('Start browser IDE')).toBeVisible();
});

// The controller total includes the provider call and the call includes its EC2 subphases.
test('counts the EC2 calls once and shows the remaining VM and controller time', () => {
  render(<MantineProvider><LaunchPerformance performance={{
    steps: [{ key: 'provision', label: 'Provision workspace', status: 'succeeded', durationMs: 5610,
      startedAt: '2026-11-20T12:00:00.000Z' }],
    events: [
      { id: 'submit', source: 'provider', phase: 'ec2_run_instances', outcome: 'succeeded', durationMs: 1020, occurredAt: '2026-11-20T12:00:01.100Z' },
      { id: 'power', source: 'provider', phase: 'vm_initial_power', outcome: 'succeeded', durationMs: 4120, occurredAt: '2026-11-20T12:00:05.240Z' },
      { id: 'create', source: 'provider', phase: 'vm_create', outcome: 'succeeded', durationMs: 5420, occurredAt: '2026-11-20T12:00:05.460Z' },
    ],
  }} /></MantineProvider>);
  const breakdown = within(screen.getByRole('table', { name: 'Launch timing details' }));
  for (const [label, amount] of [
    ['Provision workspace', '5.61 s'], ['Submit EC2 launch', '1.02 s'], ['Wait for initial EC2 power state', '4.12 s'],
    ['Other VM creation time', '280 ms'], ['Other time in step', '190 ms'],
  ]) {
    expect(within(breakdown.getByRole('row', { name: new RegExp(`^${label}`) })).getByText(amount)).toBeVisible();
  }
  expect(breakdown.queryByText('5.42 s')).not.toBeInTheDocument();
  expect(within(screen.getByRole('table', { name: 'Additional timing measurements' })).getByText('5.42 s')).toBeVisible();
});

// Overlapping attempts are charged only for the part of each interval inside the step and still available.
test('clips overlapping provider attempts to the controller step window', () => {
  render(<MantineProvider><LaunchPerformance performance={{
    steps: [{ key: 'enroll', label: 'Enroll the guest', status: 'active', durationMs: 2501,
      startedAt: '2026-11-20T12:00:01.000Z' }],
    events: [
      { id: 'seed', source: 'provider', phase: 'enrollment_delivery', outcome: 'succeeded', durationMs: 1500, occurredAt: '2026-11-20T12:00:02.000Z' },
      { id: 'boot', source: 'provider', phase: 'vm_start', outcome: 'failed', durationMs: 1500, reasonCode: 'timed_out', occurredAt: '2026-11-20T12:00:03.000Z' },
    ],
  }} /></MantineProvider>);
  const breakdown = within(screen.getByRole('table', { name: 'Launch timing details' }));
  expect(within(breakdown.getByRole('row', { name: /^Enroll the guest/ })).getByText('2.501 s')).toBeVisible();
  expect(breakdown.getAllByText('1 s')).toHaveLength(2);
  expect(breakdown.getByText('501 ms')).toBeVisible();
  expect(breakdown.getByText('Timed out')).toBeVisible();
});

// Controller handoff and serial guest durations share the elapsed budget while nested Docker timings remain recorded detail.
test('accounts for Dev Container configuration phases without double-counting Docker and image preparation', () => {
  const at = (seconds: string) => `2026-11-20T12:00:${seconds}Z`;
  render(<MantineProvider><LaunchPerformance performance={{
    steps: [{ key: 'configure', label: 'Configure the development container', status: 'succeeded', durationMs: 12000, startedAt: at('00') }],
    events: [
      { id: 'dispatch', source: 'controller', phase: 'configure_dispatched', outcome: 'observed', durationMs: 0, occurredAt: at('02') },
      { id: 'checkout', source: 'guest', phase: 'repository_preparation', outcome: 'succeeded', durationMs: 1200, occurredAt: at('04') },
      { id: 'resolve', source: 'guest', phase: 'image_configuration', outcome: 'succeeded', durationMs: 100, occurredAt: at('04') },
      { id: 'fetch', source: 'guest', phase: 'image_prefetch', outcome: 'succeeded', durationMs: 900, occurredAt: at('05') },
      { id: 'image', source: 'guest', phase: 'image_prepare', outcome: 'succeeded', durationMs: 1000, occurredAt: at('05') },
      { id: 'docker', source: 'guest', phase: 'docker_image_build', outcome: 'observed', durationMs: 2000, occurredAt: at('09') },
      { id: 'up', source: 'guest', phase: 'initial_devcontainer_setup', outcome: 'succeeded', durationMs: 4000, occurredAt: at('09') },
      { id: 'extension', source: 'guest', phase: 'ide_extensions', outcome: 'failed', durationMs: 1300, occurredAt: at('10') },
      { id: 'report', source: 'controller', phase: 'configure_reported', outcome: 'observed', durationMs: 0, occurredAt: at('11') },
    ],
  }} /></MantineProvider>);
  const table = within(screen.getByRole('table', { name: 'Launch timing details' }));
  for (const [phase, time] of [
    ['Wait for guest setup dispatch', '2 s'], ['Prepare repository', '1.2 s'], ['Resolve image configuration', '100 ms'],
    ['Prefetch development image', '900 ms'], ['Run initial Dev Container setup', '4 s'], ['Install IDE extensions', '1.3 s'],
    ['Confirm guest setup', '1 s'], ['Other time in step', '1.5 s'],
  ]) {
    expect(within(table.getByRole('row', { name: new RegExp(`^${phase}`) })).getByText(time)).toBeVisible();
  }
  expect(table.queryByText('Build image')).not.toBeInTheDocument();
  expect(within(screen.getByRole('table', { name: 'Additional timing measurements' })).getByText('Build image')).toBeVisible();
});

// A guest may report a duration larger than the wall-clock budget, so retain that sample as a diagnostic.
test('retains out-of-budget guest times without charging them to the parent', () => {
  render(<MantineProvider><LaunchPerformance performance={{
    steps: [{ key: 'configure', label: 'Configure the development container', status: 'succeeded', durationMs: 3000, startedAt: '2026-11-20T12:00:00Z' }],
    events: [
      { id: 'dispatch', source: 'controller', phase: 'configure_dispatched', outcome: 'observed', durationMs: 0, occurredAt: '2026-11-20T12:00:01Z' },
      { id: 'slow', source: 'guest', phase: 'ide_extensions', outcome: 'failed', durationMs: 7000, occurredAt: '2026-11-20T12:00:04Z' },
      { id: 'report', source: 'controller', phase: 'configure_reported', outcome: 'observed', durationMs: 0, occurredAt: '2026-11-20T12:00:02Z' },
    ],
  }} /></MantineProvider>);
  expect(within(screen.getByRole('table', { name: 'Launch timing details' })).queryByText('Install IDE extensions')).not.toBeInTheDocument();
  const diagnostic = within(screen.getByRole('table', { name: 'Additional timing measurements' }));
  expect(within(diagnostic.getByRole('row', { name: /^Install IDE extensions/ })).getByText('7 s')).toBeVisible();
});

// The demo guest's duration fits its parent, even though it exceeds the controller's handoff window.
test('keeps measured guest phases when controller handoff would consume the same time', () => {
  const at = (seconds: string) => `2026-11-20T12:00:${seconds}Z`;
  render(<MantineProvider><LaunchPerformance performance={{
    steps: [{ key: 'configure', label: 'Configure the development container', status: 'succeeded', durationMs: 26014, startedAt: at('00') }],
    events: [
      { id: 'dispatch', source: 'controller', phase: 'configure_dispatched', outcome: 'observed', durationMs: 0, occurredAt: at('00.848') },
      { id: 'setup', source: 'guest', phase: 'initial_devcontainer_setup', outcome: 'succeeded', durationMs: 19000, occurredAt: at('20') },
      { id: 'hooks', source: 'guest', phase: 'required_hooks', outcome: 'succeeded', durationMs: 5747, occurredAt: at('25') },
      { id: 'report', source: 'controller', phase: 'configure_reported', outcome: 'observed', durationMs: 0, occurredAt: at('25.139') },
    ],
  }} /></MantineProvider>);
  const table = within(screen.getByRole('table', { name: 'Launch timing details' }));
  expect(table.getByRole('row', { name: /^Run initial Dev Container setup.*19 s$/ })).toBeVisible();
  expect(table.getByRole('row', { name: /^Run required hooks.*5\.747 s$/ })).toBeVisible();
  expect(table.getByRole('row', { name: /^Other time in step.*1\.267 s$/ })).toBeVisible();
  expect(table.queryByText('Wait for guest setup dispatch')).not.toBeInTheDocument();
  expect(table.queryByText('Confirm guest setup')).not.toBeInTheDocument();
});
