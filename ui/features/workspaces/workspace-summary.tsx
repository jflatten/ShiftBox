import { Chip, Group, Loader, rem } from '@mantine/core';
import { workspaceState } from './workspace';

// Carbon status colors distinguish progress, healthy, inactive, and warning states.
const progress = '#0043ce';
const inactive = '#6f6f6f';
const unknown = '#8a3ffc';
const running = '#24a148';
const unreachable = '#ff832b';
const chipHeight = 23;
const statusColors = new Map([
  ['running', running],
  ['provisioning', progress], ['starting', progress], ['stopping', progress], ['deleting', progress],
  ['stopped', inactive], ['deleted', inactive],
  ['unreachable', unreachable], ['failed', '#da1e28'],
]);

/** Report observed state without implying that a running VM has a ready IDE. */
export function WorkspaceStatus({ value, transitioning = false }: { value: string; transitioning?: boolean }) {
  const color = statusColors.get(value) ?? unknown;
  const textColor = color === running || color === unreachable ? '#161616' : '#ffffff';
  const chip = (
    <Chip checked readOnly hidden icon={false} color={color} variant="filled" size="xs"
      vars={() => ({ root: { '--chip-size': rem(chipHeight) } })}
      styles={{ label: { color: textColor, pointerEvents: 'none' } }}>
      {workspaceState(value)}
    </Chip>
  );
  return transitioning ? <Group gap="xs" wrap="nowrap">
    {chip}
    <Loader type="oval" size={chipHeight} role="status" aria-label="State transition in progress" />
  </Group> : chip;
}

export function WorkspaceTime({ value, fallback }: { value: string | null; fallback: string }) {
  if (!value) return fallback;
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return fallback;
  return <time dateTime={value}>{date.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })}</time>;
}
