import { useState } from 'react';
import { ActionIcon, Code, Group, Modal, Stack, Text, Tooltip, VisuallyHidden } from '@mantine/core';
import { useClipboard } from '@mantine/hooks';
import { useQuery } from '@tanstack/react-query';

const sshLabel = 'Connect with SSH';
const copyLabel = 'Copy SSH command';
const tooltipEvents = { hover: true, focus: true, touch: false };

export function WorkspaceSSHService({ workspaceId, accountId }: { workspaceId: string; accountId: string }) {
  const alias = useQuery({
    queryKey: ['workspaces', 'ssh', accountId, workspaceId],
    refetchInterval: 30_000,
    queryFn: async ({ signal }): Promise<string | null> => {
      const params = new URLSearchParams({ workspace: workspaceId, account: accountId });
      const options = {
        signal: AbortSignal.any([signal, AbortSignal.timeout(8_000)]), credentials: 'omit',
        cache: 'no-store', redirect: 'error', targetAddressSpace: 'loopback',
      } as const;
      try {
        const response = await fetch(`http://127.0.0.1:48652/v1/ssh-alias?${params}`, options);
        if (!response.ok) return null;
        const status = await response.json() as { alias?: unknown } | null;
        const alias = status?.alias;
        return typeof alias === 'string' && /^shiftbox-[a-z0-9-]{1,54}$/.test(alias) && alias === alias.trim() ? alias : null;
      } catch {
        // The local helper is optional. Its absence leaves other services available.
        return null;
      }
    },
  });
  return alias.data ? <SSHCommand key={alias.data} command={`ssh ${alias.data}`} /> : null;
}

function SSHCommand({ command }: { command: string }) {
  const [opened, setOpened] = useState(false);
  const clipboard = useClipboard({ timeout: 2_000 });
  return <>
    <Tooltip label={sshLabel} events={tooltipEvents}>
      <ActionIcon variant="subtle" size={32} aria-label={sshLabel} aria-haspopup="dialog" onClick={() => setOpened(true)}>
        <svg aria-hidden="true" width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5">
          <rect x="2.75" y="4.75" width="18.5" height="14.5" rx="1" />
          <path d="m6.5 8.5 3 3-3 3m5.5 0h4.5" />
        </svg>
      </ActionIcon>
    </Tooltip>
    <Modal opened={opened} onClose={() => { setOpened(false); clipboard.reset(); }} title={sshLabel}
      closeButtonProps={{ 'aria-label': 'Close SSH dialog' }} transitionProps={{ duration: 0 }}>
      <Stack gap="sm">
        <Text size="sm">Paste this command into your terminal.</Text>
        <Group gap="xs" wrap="nowrap" align="center">
          <Code block style={{ flex: 1, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{command}</Code>
          <Tooltip label={clipboard.copied ? 'Copied' : copyLabel} events={tooltipEvents}>
            <ActionIcon variant="default" size={34} aria-label={copyLabel} onClick={() => clipboard.copy(command)} data-autofocus>
              <svg aria-hidden="true" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5">
                {clipboard.copied ? <path d="m5 12 4 4L19 6" /> :
                  <><rect x="8" y="8" width="12" height="12" rx="1" /><path d="M16 5V4H4v12h1" /></>}
              </svg>
            </ActionIcon>
          </Tooltip>
        </Group>
        <VisuallyHidden role="status">{clipboard.copied ? 'Copied' : ''}</VisuallyHidden>
        {clipboard.error ? <Text size="sm" role="alert">Clipboard access is unavailable. Select the command to copy it manually.</Text> : null}
      </Stack>
    </Modal>
  </>;
}
