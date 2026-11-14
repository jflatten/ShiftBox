import { Alert, Stack, Text } from '@mantine/core';
import type { Workspace } from './workspace';

export function RetentionWarning({ workspace }: { workspace: Workspace }) {
  const retention = workspace.retention;
  if (!retention || workspace.observedState === 'deleted' ||
      (retention.state === 'disabled' && workspace.desiredState !== 'deleted')) return null;
  const committed = workspace.desiredState === 'deleted';
  const paused = !committed && (retention.state === 'paused' || retention.state === 'grace');
  const grace = retention.graceUntil && new Date(retention.graceUntil);
  return (
    <Alert color={committed ? 'red' : 'yellow'}
      title={committed ? 'Workspace deletion is in progress' : paused ? 'Retention cleanup paused' : 'Automatic retention cleanup'}>
      <Stack gap="xs">
        {paused ? (
          grace && Number.isFinite(grace.getTime()) ? (
            <Text size="sm">Recovery grace ends <time dateTime={retention.graceUntil}>{grace.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })}</time>.</Text>
          ) : <Text size="sm">Cleanup resumes after the controller verifies the provider and recovery grace elapses.</Text>
        ) : null}
        <Text size="sm">Deletion removes project files, the developer home, and persistent mounts on the workspace disk. Management backups exclude workspace disks.</Text>
      </Stack>
    </Alert>
  );
}
