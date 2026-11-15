import { Badge } from '@mantine/core';
import { workspaceState } from './workspace';

/** Report observed state without implying that a running VM has a ready IDE. */
export function WorkspaceStatus({ value }: { value: string }) {
  const color = value === 'running' ? 'green' : value === 'failed' || value === 'unreachable' ? 'red' :
    ['starting', 'stopping', 'provisioning', 'deleting'].includes(value) ? 'yellow' : 'gray';
  return <Badge color={color} variant="light">{workspaceState(value)}</Badge>;
}

export function WorkspaceTime({ value, fallback }: { value: string | null; fallback: string }) {
  if (!value) return fallback;
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return fallback;
  return <time dateTime={value}>{date.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })}</time>;
}
