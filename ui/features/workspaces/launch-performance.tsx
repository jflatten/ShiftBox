import { Paper, Stack, Table, Text, Title } from '@mantine/core';
import { useOperation } from '../../lib/use-operation';
import { configureBreakdown, isConfigureBoundary, providerBreakdown, type Performance, type TimingEvent } from './launch-performance-breakdown';
import { WorkspaceStatus } from './workspace-summary';

const phases: Record<string, [stepKey: string, label: string]> = {
  vm_create: ['provision', 'Create workspace VM'],
  vm_initial_power: ['provision', 'Wait for initial EC2 power state'],
  ec2_run_instances: ['provision', 'Submit EC2 launch'],
  enrollment_delivery: ['enroll', 'Deliver enrollment media'],
  vm_start: ['enroll', 'Start workspace VM'],
  vm_running: ['provision', 'Provider confirmed VM running'],
  guest_authenticated: ['enroll', 'First authenticated guest poll'],
  configure_dispatched: ['configure', 'Wait for guest setup dispatch'],
  configure_reported: ['configure', 'Confirm guest setup'],
  repository_preparation: ['configure', 'Prepare repository'],
  image_prepare: ['configure', 'Prepare development image'],
  image_configuration: ['configure', 'Resolve image configuration'],
  image_prefetch: ['configure', 'Prefetch development image'],
  docker_image_pull: ['configure', 'Pull image'],
  docker_image_build: ['configure', 'Build image'],
  docker_container_run: ['configure', 'Run container'],
  docker_container_create: ['configure', 'Create container'],
  docker_container_start: ['configure', 'Start container'],
  docker_container_ready: ['configure', 'Container running'],
  devcontainer_up: ['configure', 'Run Dev Container setup'],
  initial_devcontainer_setup: ['configure', 'Run initial Dev Container setup'],
  recreate_with_retained_home: ['configure', 'Recreate container with retained home'],
  retained_home: ['configure', 'Attach retained home'],
  project_permissions: ['configure', 'Set project permissions'],
  required_hooks: ['configure', 'Run required hooks'],
  remaining_hooks: ['finish_setup', 'Run remaining hooks'],
  ide_mount: ['configure', 'Mount browser IDE'],
  ide_install: ['configure', 'Install browser IDE'],
  ide_settings: ['configure', 'Apply IDE settings'],
  ide_extensions: ['configure', 'Install IDE extensions'],
  ide_start: ['configure', 'Start browser IDE'],
};

const sourceLabels = { controller: 'Controller', provider: 'VM provider', guest: 'Guest reported' };
const results = {
  pending: { label: 'Pending', color: 'stopped' },
  active: { label: 'In progress', color: 'vm-running' },
  skipped: { label: 'Skipped', color: 'stopped' },
  succeeded: { label: 'Completed', color: 'ready' },
  failed: { label: 'Failed', color: 'failed' },
  observed: { label: 'Observed', color: 'vm-running' },
};

const failureReasons: Record<string, string> = {
  unavailable: 'Provider unavailable',
  outcome_unknown: 'Provider outcome unknown',
  resource_missing: 'Resource missing',
  ownership_mismatch: 'Resource ownership mismatch',
  permission_denied: 'Permission denied',
  unsupported: 'Unsupported provider capability',
  invalid_state: 'Invalid provider state',
  partially_completed: 'Provider action partially completed',
  timed_out: 'Timed out',
  cancelled: 'Cancelled',
};

function duration(milliseconds: number) {
  if (milliseconds < 1000) return `${milliseconds} ms`;
  return `${new Intl.NumberFormat(undefined, { maximumFractionDigits: 3 }).format(milliseconds / 1000)} s`;
}

function bytes(value: number) {
  if (value < 1024) return `${value} B`;
  const mib = value >= 1024 ** 2;
  return `${new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 }).format(value / (mib ? 1024 ** 2 : 1024))} ${mib ? 'MiB' : 'KiB'}`;
}

function Measurement({ label, value }: { label: string; value: string }) {
  return <div>
    <Text component="dt" size="sm" c="dimmed">{label}</Text>
    <Text component="dd" ml={0} fw={600}>{value}</Text>
  </div>;
}

function isMilestone(event: TimingEvent) {
  return event.phase === 'vm_running' || event.phase === 'guest_authenticated';
}

function TimingHeaders({ elapsed }: { elapsed?: boolean }) {
  return <Table.Thead>
    <Table.Tr><Table.Th scope="col">Phase</Table.Th><Table.Th scope="col">Source</Table.Th>
      <Table.Th scope="col">Result</Table.Th><Table.Th scope="col" ta="right">{elapsed ? 'Elapsed time' : 'Recorded duration'}</Table.Th></Table.Tr>
  </Table.Thead>;
}

function TimingEventRow({ event, elapsedMs }: { event: TimingEvent; elapsedMs?: number }) {
  const result = results[event.outcome];
  const reason = event.reasonCode ? failureReasons[event.reasonCode] : undefined;
  const label = event.phase === 'vm_create' && elapsedMs !== undefined && elapsedMs < event.durationMs
    ? 'Other VM creation time' : phases[event.phase]?.[1] ?? 'Launch phase';
  return <Table.Tr>
    <Table.Th scope="row" fw={500} style={{ paddingInlineStart: 'var(--mantine-spacing-xl)' }}>
      {label}
    </Table.Th>
    <Table.Td>{sourceLabels[event.source]}</Table.Td>
    <Table.Td><WorkspaceStatus value={result.color} label={result.label} />
      {reason ? <Text size="xs" c="dimmed">{reason}</Text> : null}</Table.Td>
    <Table.Td ta="right">{isMilestone(event) ? '—' : duration(elapsedMs ?? event.durationMs)}</Table.Td>
  </Table.Tr>;
}

export function LaunchPerformance({ performance }: { performance: Performance }) {
  const { timeToIdeReadyMs, timeToSetupCompleteMs, vmRunningToGuestContactMs, steps, events, cache } = performance;
  const eventsByStep = new Map<string, TimingEvent[]>(steps.map(step => [step.key, []]));
  const ungrouped: TimingEvent[] = [];
  // Single-boot EC2 launches confirm VM power during provisioning. If the
  // provider boots the VM separately after delivering enrollment media, that
  // same observation belongs to enrollment.
  const bootsDuringEnrollment = events.some(event => event.phase === 'vm_start' || event.phase === 'enrollment_delivery');
  for (const event of events) {
    let stepKey = phases[event.phase]?.[0];
    if (event.phase === 'vm_running' && bootsDuringEnrollment) stepKey = 'enroll';
    if (stepKey === 'finish_setup' && !eventsByStep.has('finish_setup')) stepKey = 'configure';
    const group = stepKey ? eventsByStep.get(stepKey) : undefined;
    (group ?? ungrouped).push(event);
  }
  const timings = steps.map(step => {
    const events = eventsByStep.get(step.key) ?? [];
    const configure = step.key === 'configure';
    const { elapsed, otherMs } = configure ? configureBreakdown(step, events) : providerBreakdown(step, events);
    const diagnostics = events.filter(event => !isMilestone(event) && !isConfigureBoundary(event) && elapsed.get(event) !== event.durationMs);
    const rows = configure ? Array.from(elapsed.keys()) : events.filter(event => elapsed.has(event) || isMilestone(event));
    const showMissingGuestTimings = configure && (step.status === 'succeeded' || step.status === 'failed') &&
      !events.some(event => event.source === 'guest');
    return { step, rows, elapsed, otherMs, diagnostics, showMissingGuestTimings };
  });
  const diagnosticGroups = timings
    .filter(timing => timing.diagnostics.length > 0)
    .map(({ step, diagnostics }) => ({ key: step.key, label: step.label, events: diagnostics }));
  if (ungrouped.length) diagnosticGroups.push({ key: 'other', label: 'Other launch timings', events: ungrouped });

  const readiness = timeToIdeReadyMs === undefined
    ? steps.some(step => step.status === 'failed' || step.status === 'skipped') ? 'Verification unavailable' : 'Pending verification'
    : duration(timeToIdeReadyMs);
  return (
    <Paper component="section" aria-label="Launch performance" withBorder radius="md" p="md" w="100%">
      <Stack gap="md">
        <Title order={2} size="h4">Launch performance</Title>
        <Stack component="dl" m={0} gap="xs">
          <Measurement label="Time to IDE ready" value={readiness} />
          {timeToSetupCompleteMs !== undefined ? <Measurement label="Time to setup complete" value={duration(timeToSetupCompleteMs)} /> : null}
          {vmRunningToGuestContactMs !== undefined ? <Measurement label="VM running to guest contact" value={duration(vmRunningToGuestContactMs)} /> : null}
        </Stack>
        {steps.length ? (
          <Table.ScrollContainer minWidth={450}>
            <Table tabularNums aria-label="Launch timing details">
              <TimingHeaders elapsed />
              {timings.map(({ step, rows, elapsed, otherMs, showMissingGuestTimings }) => {
                const result = results[step.status];
                return <Table.Tbody key={step.key} aria-label={step.label}>
                  <Table.Tr bg="var(--mantine-color-default-hover)">
                    <Table.Th scope="rowgroup" fw={600}>{step.label}</Table.Th><Table.Td>Controller</Table.Td>
                    <Table.Td><WorkspaceStatus value={result.color} label={result.label} /></Table.Td>
                    <Table.Td ta="right" fw={600}>{step.status === 'pending' || step.status === 'skipped' ? '—' : duration(step.durationMs)}</Table.Td>
                  </Table.Tr>
                  {rows.map(event => (
                    <TimingEventRow key={`${event.source}/${event.id}`} event={event} elapsedMs={elapsed.get(event)} />
                  ))}
                  {elapsed.size && otherMs > 0 ? (
                    <Table.Tr>
                      <Table.Th scope="row" fw={500} style={{ paddingInlineStart: 'var(--mantine-spacing-xl)' }}>Other time in step</Table.Th>
                      <Table.Td colSpan={2} />
                      <Table.Td ta="right">{duration(otherMs)}</Table.Td>
                    </Table.Tr>
                  ) : null}
                  {showMissingGuestTimings ? <Table.Tr>
                    <Table.Td colSpan={4}>
                      <Text size="sm" c="dimmed" pl="lg">Guest timing samples are unavailable for this launch.</Text>
                    </Table.Td>
                  </Table.Tr> : null}
                </Table.Tbody>;
              })}
            </Table>
          </Table.ScrollContainer>
        ) : null}
        {diagnosticGroups.length ? <Stack gap="xs">
          <Title order={3} size="h5">Additional timing measurements</Title>
          <Text size="sm" c="dimmed">Raw measurements can overlap with each other or span multiple controller steps.</Text>
          <Table.ScrollContainer minWidth={450}>
            <Table tabularNums aria-label="Additional timing measurements">
              <TimingHeaders />
              {diagnosticGroups.map(group => <Table.Tbody key={group.key} aria-label={group.label}>
                <Table.Tr bg="var(--mantine-color-default-hover)">
                  <Table.Th scope="rowgroup" colSpan={4} fw={600}>{group.label}</Table.Th>
                </Table.Tr>
                {group.events.map(event => <TimingEventRow key={`${event.source}/${event.id}`} event={event} />)}
              </Table.Tbody>)}
            </Table>
          </Table.ScrollContainer>
        </Stack> : null}
        {cache ? <Stack gap="xs">
          <Title order={3} size="h5">Image cache</Title>
          <Stack component="dl" m={0} gap="xs">
            <Measurement label="Served from cache" value={bytes(cache.cachedBytes)} />
            <Measurement label="Fetched upstream" value={bytes(cache.upstreamBytes)} />
            <Measurement label="Registry request window" value={duration(cache.windowMs)} />
            <Measurement label="Requests serving cached bytes" value={`${cache.cachedRequests} of ${cache.requests}`} />
            {cache.failedRequests > 0 ? <Measurement label="Failed requests" value={String(cache.failedRequests)} /> : null}
            {cache.rateLimitedRequests > 0 ? <Measurement label="Rate-limited requests" value={String(cache.rateLimitedRequests)} /> : null}
          </Stack>
        </Stack> : null}
      </Stack>
    </Paper>
  );
}

export function HistoricalLaunchPerformance({ operationId }: { operationId: string }) {
  const operation = useOperation(operationId);
  if (operation.accessDenied) return null;
  if (operation.isPending) return <Text role="status">Loading launch performance…</Text>;
  if (operation.data?.launchPerformance) return <LaunchPerformance performance={operation.data.launchPerformance} />;
  if (operation.isError) return <Text role="alert">Launch performance is unavailable.</Text>;
  return null;
}
