import { Chip, rem } from '@mantine/core';
import { workspaceState } from './workspace';

// Carbon status colors distinguish progress, healthy, inactive, and warning states.
const progress = '#0043ce';
const inactive = '#6f6f6f';
const unknown = '#8a3ffc';
const healthy = '#24a148';
const error = '#da1e28';
const unreachable = '#ff832b';
const chipHeight = 23;
const statusColors = new Map([
  ['running', healthy], ['healthy', healthy],
  ['provisioning', progress], ['starting', progress], ['stopping', progress], ['deleting', progress],
  ['stopped', inactive], ['deleted', inactive],
  ['unreachable', unreachable], ['failed', error], ['error', error],
]);

/** Display an observed workspace or probe status as a read-only chip. */
export function WorkspaceStatus({ value, label }: { value: string; label?: string }) {
  const color = statusColors.get(value) ?? unknown;
  const textColor = color === healthy || color === unreachable ? '#161616' : '#ffffff';
  return (
    <Chip checked readOnly hidden icon={false} color={color} variant="filled" size="xs"
      vars={() => ({ root: { '--chip-size': rem(chipHeight) } })}
      styles={{ label: { color: textColor, pointerEvents: 'none' } }}>
      {label ?? workspaceState(value)}
    </Chip>
  );
}

export function WorkspaceTime({ value, fallback }: { value: string | null; fallback: string }) {
  if (!value) return fallback;
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return fallback;
  return <time dateTime={value}>{date.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })}</time>;
}
