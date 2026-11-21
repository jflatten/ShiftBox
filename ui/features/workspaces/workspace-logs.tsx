import { useEffect, useRef, useState } from 'react';
import { Box, Button, Chip, Group, Paper, ScrollArea, Stack, Text, Title } from '@mantine/core';
import type { operations } from '../../lib/api-types';

type LogBatch = operations['streamWorkspaceLogs']['responses'][200]['content']['text/event-stream'];
type Connection = 'connecting' | 'live' | 'reconnecting' | 'disconnected';
const connectionState = {
  connecting: { label: 'Connecting…', color: 'gray' },
  live: { label: 'Live', color: 'green' },
  reconnecting: { label: 'Reconnecting…', color: 'yellow' },
  disconnected: { label: 'Disconnected', color: 'red' },
} as const;

const scrollbackLimit = 512 * 1024;

export function WorkspaceLogs({ workspaceId }: { workspaceId: string }) {
  const [output, setOutput] = useState('');
  const [connection, setConnection] = useState<Connection>('connecting');
  const [following, setFollowing] = useState(true);
  const [retry, setRetry] = useState(0);
  const cursor = useRef('');
  const viewport = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const source = new EventSource(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/logs`);
    source.addEventListener('open', () => setConnection('live'));
    source.addEventListener('error', () => {
      setConnection(source.readyState === EventSource.CLOSED ? 'disconnected' : 'reconnecting');
    });
    source.addEventListener('workspace.log', (event: MessageEvent<string>) => {
      try {
        const batch: LogBatch = JSON.parse(event.data);
        if (!event.lastEventId || Number(event.lastEventId) <= Number(cursor.current) || typeof batch?.text !== 'string') return;
        cursor.current = event.lastEventId;
        setOutput(current => (current + batch.text).slice(-scrollbackLimit));
      } catch {
        // Ignore malformed events without interrupting log delivery.
      }
    });
    return () => source.close();
  }, [workspaceId, retry]);

  useEffect(() => {
    if (following && viewport.current) viewport.current.scrollTop = viewport.current.scrollHeight;
  }, [following, output]);

  const status = connectionState[connection];
  return (
    <Paper component="section" aria-label="Workspace logs" withBorder radius="md" p="md">
      <Stack gap="sm">
        <Group justify="space-between">
          <Group gap="sm">
            <Title order={2} size="h4">Dev Container logs</Title>
            <Box role="status">
              <Chip checked readOnly hidden icon={false} color={status.color} variant="filled" size="xs">{status.label}</Chip>
            </Box>
          </Group>
          <Group gap="xs">
            {connection === 'disconnected' ? (
              <Button size="compact-sm" variant="default" onClick={() => {
                setConnection('connecting');
                setRetry(value => value + 1);
              }}>Reconnect</Button>
            ) : null}
            <Button size="compact-sm" variant="default" onClick={() => setFollowing(value => !value)}>
              {following ? 'Pause auto-scroll' : 'Resume auto-scroll'}
            </Button>
          </Group>
        </Group>
        <ScrollArea h={420} type="auto" offsetScrollbars viewportRef={viewport}
          viewportProps={{ tabIndex: 0, role: 'region', 'aria-label': 'Dev Container output' }}
          onScrollPositionChange={({ y }) => {
            const view = viewport.current;
            if (view && view.scrollHeight - view.clientHeight - y > 40) setFollowing(false);
          }}
          bg="black" c="var(--mantine-color-gray-1)" ff="monospace" style={{ borderRadius: 'var(--mantine-radius-sm)' }}>
          {output ? (
            <Box component="pre" ff="inherit" fz="sm" m={0} p="sm"
              style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{output}</Box>
          ) : (
            <Text ff="inherit" size="sm" p="sm">
              {connection === 'disconnected' ? 'Unable to open the log stream.' : 'Waiting for Dev Container output…'}
            </Text>
          )}
        </ScrollArea>
        <Text size="xs" c="dimmed">Shows recent setup output from this controller session.</Text>
      </Stack>
    </Paper>
  );
}
