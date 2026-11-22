import type { components } from '../../lib/api-types';

export type Performance = components['schemas']['LaunchPerformance'];
export type TimingEvent = Performance['events'][number];
type Step = Performance['steps'][number];
type Window = { start: number; end: number };

// These guest phases run serially. The CLI's Docker calls and retained-home
// recreation are already included in their enclosing phases.
const configurePhases = new Set([
  'repository_preparation', 'image_prepare', 'image_configuration', 'image_prefetch',
  'initial_devcontainer_setup', 'retained_home', 'project_permissions', 'required_hooks',
  'ide_mount', 'ide_install', 'ide_settings', 'ide_extensions', 'ide_start',
]);

export function isConfigureBoundary(event: TimingEvent) {
  return event.source === 'controller' && (event.phase === 'configure_dispatched' || event.phase === 'configure_reported');
}

export function configureBreakdown(step: Step, events: readonly TimingEvent[]) {
  const elapsed = new Map<TimingEvent, number>();
  const start = Date.parse(step.startedAt ?? '');
  const end = start + step.durationMs;
  let otherMs = step.durationMs;
  if (!Number.isFinite(start) || step.status === 'pending' || step.status === 'skipped') return { elapsed, otherMs };

  const dispatched = events.find(event => isConfigureBoundary(event) && event.phase === 'configure_dispatched');
  if (!dispatched) return { elapsed, otherMs };
  // The poll clock may be sampled just before the controller opens the step.
  const dispatchTime = Math.max(start, Date.parse(dispatched.occurredAt));
  const reported = events.find(event => isConfigureBoundary(event) && event.phase === 'configure_reported');
  const reportTime = reported ? Date.parse(reported.occurredAt) : end;
  if (!(dispatchTime <= reportTime && reportTime <= end)) return { elapsed, otherMs };

  const guestSamples = new Map<string, TimingEvent[]>();
  for (const event of events) {
    if (event.source !== 'guest' || !configurePhases.has(event.phase)) continue;
    const samples = guestSamples.get(event.phase) ?? [];
    samples.push(event);
    guestSamples.set(event.phase, samples);
  }
  if (guestSamples.has('image_configuration') || guestSamples.has('image_prefetch')) guestSamples.delete('image_prepare');
  const serial = Array.from(guestSamples.values()).filter(samples => samples.length === 1).map(([sample]) => sample);
  const guestMs = serial.reduce((total, event) => total + event.durationMs, 0);
  const guestFitsStep = guestMs <= step.durationMs;
  // Guest durations and controller timestamps come from different clocks.
  // Keep measured phases that fit the parent and leave conflicting handoff time unattributed.
  const showHandoff = guestMs <= reportTime - dispatchTime || !guestFitsStep;
  if (showHandoff) {
    elapsed.set(dispatched, dispatchTime - start);
    otherMs -= dispatchTime - start;
  }
  if (guestFitsStep) {
    for (const event of serial) elapsed.set(event, event.durationMs);
    otherMs -= guestMs;
  }
  if (reported && showHandoff) {
    elapsed.set(reported, end - reportTime);
    otherMs -= end - reportTime;
  }
  return { elapsed, otherMs };
}

export function providerBreakdown(step: Step, events: readonly TimingEvent[]) {
  const elapsed = new Map<TimingEvent, number>();
  const start = Date.parse(step.startedAt ?? '');
  if (!Number.isFinite(start) || step.durationMs <= 0 || step.status === 'pending' || step.status === 'skipped') {
    return { elapsed, otherMs: step.durationMs };
  }
  let remaining: Window[] = [{ start, end: start + step.durationMs }];
  let otherMs = step.durationMs;

  // Provider completion times and step starts share the controller clock.
  // Give the inner EC2 phases first claim on their time inside VM creation.
  const spans = events.flatMap(event => {
    if (event.source !== 'provider' || event.durationMs <= 0) return [];
    const end = Date.parse(event.occurredAt);
    return Number.isFinite(end) ? [{ event, start: end - event.durationMs, end }] : [];
  }).sort((a, b) => Number(a.event.phase === 'vm_create') - Number(b.event.phase === 'vm_create') || a.start - b.start);

  for (const span of spans) {
    let credited = 0;
    const available: Window[] = [];
    for (const window of remaining) {
      const from = Math.max(span.start, window.start);
      const to = Math.min(span.end, window.end);
      if (from >= to) {
        available.push(window);
        continue;
      }
      credited += to - from;
      if (window.start < from) available.push({ start: window.start, end: from });
      if (to < window.end) available.push({ start: to, end: window.end });
    }

    if (credited > 0) elapsed.set(span.event, credited);
    otherMs -= credited;
    remaining = available;
  }
  return { elapsed, otherMs };
}
