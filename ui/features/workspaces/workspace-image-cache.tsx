import { Alert, Stack, Text } from '@mantine/core';
import { WorkspaceStatus } from './workspace-summary';
import type { Workspace } from './workspace';

export function WorkspaceImageCache({ cache }: { cache?: Workspace['imageCache'] }) {
  if (!cache?.warningActive && !cache?.updateAvailable) return null;
  return (
    <Alert color={cache.warningActive ? 'orange' : 'blue'} title="Image freshness">
      <Stack gap="xs" align="flex-start">
        {cache.warningActive ? (
          <>
            <WorkspaceStatus value="unreachable" label="Cached version" />
            <Text size="sm">The upstream registry could not be contacted. Using cached version.</Text>
          </>
        ) : null}
        {cache.updateAvailable ? <Text size="sm">A newer upstream image is available.</Text> : null}
        {(cache.references ?? []).filter(ref => ref.warningActive || (ref.verifiedDigest && ref.resolvedDigest !== ref.verifiedDigest))
          .map(ref => (
            <Stack gap={0} key={`${ref.origin}/${ref.repository}:${ref.tag}`}>
              <Text fw={600} size="sm">{ref.origin}/{ref.repository}:{ref.tag}</Text>
              <Text size="sm" style={{ overflowWrap: 'anywhere' }}>Resolved at startup <Text span size="sm" ff="monospace">{ref.resolvedDigest}</Text></Text>
              {ref.verifiedDigest && ref.verifiedDigest !== ref.resolvedDigest ? (
                <Text size="sm" style={{ overflowWrap: 'anywhere' }}>Verified upstream <Text span size="sm" ff="monospace">{ref.verifiedDigest}</Text></Text>
              ) : null}
            </Stack>
          ))}
      </Stack>
    </Alert>
  );
}
