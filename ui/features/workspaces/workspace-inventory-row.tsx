import { useRef, useState } from 'react';
import { ActionIcon, Alert, Anchor, Button, Image, Menu, Modal, Stack, Table, Text, Tooltip } from '@mantine/core';
import { useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { DeleteWorkspaceDialog, WorkspaceProgress } from './workspace-detail';
import { availableWorkspaceCommands, openWorkspaceEntry, useWorkspaceCommand, useWorkspaceDetail, type WorkspaceService } from './workspace-commands';
import { workspaceName, type Workspace } from './workspace';
import { WorkspaceLifecycleStatus, WorkspaceTime } from './workspace-summary';

const openCodeServerLabel = 'Open VS Code in a new tab';

export function WorkspaceInventoryRow({ row, href, csrfToken }: { row: Workspace; href: string; csrfToken: string }) {
  const client = useQueryClient();
  const trigger = useRef<HTMLButtonElement>(null);
  const [opened, setOpened] = useState(false);
  const [dialog, setDialog] = useState<'delete' | 'progress'>();
  const [operationID, setOperationID] = useState<string>();
  const [openingService, setOpeningService] = useState(false);
  const [entryError, setEntryError] = useState<unknown>();
  const detail = useWorkspaceDetail(row.id, opened || dialog === 'delete');
  const workspace = !detail.isError ? detail.data : undefined;
  const commands = availableWorkspaceCommands(workspace);
  const lifecycle = commands.lifecycle;
  const command = useWorkspaceCommand(workspace, csrfToken, detail.refetch, (action, id) => {
    setOperationID(id);
    void client.invalidateQueries({ queryKey: ['workspaces', 'list'] });
    if (action === 'delete') close();
    if (action === 'stop' && commands.interrupt) setDialog('progress');
  });
  const currentID = workspace && (opened || dialog === 'delete') ? workspace.currentOperationId : row.currentOperationId;
  const progressID = currentID ?? operationID;
  const busy = !!currentID || !!command.submitting;
  const name = workspaceName(row);

  function close() {
    setDialog(undefined);
    trigger.current?.focus();
  }

  async function openEntry(service: WorkspaceService) {
    if (openingService) return;
    setOpeningService(true);
    setEntryError(undefined);
    try {
      if (!await openWorkspaceEntry(row.id, csrfToken, service)) setEntryError('popup-blocked');
    } catch (error) {
      setEntryError(error);
    } finally {
      setOpeningService(false);
    }
  }

  return (
    <Table.Tr>
      <Table.Td style={{ maxWidth: 250, overflowWrap: 'anywhere' }}>
        <Anchor fw={600} href={href}>{name}</Anchor>
      </Table.Td>
      <Table.Td>
        <WorkspaceLifecycleStatus compact lifecycle={row.lifecycle} />
      </Table.Td>
      <Table.Td><Stack gap={2}>
        <Text size="sm">{row.resources.cpuCount} vCPU · {row.resources.memoryMiB.toLocaleString()} MiB memory</Text>
        <Text size="xs" c="dimmed">{row.resources.diskMiB.toLocaleString()} MiB disk</Text>
      </Stack></Table.Td>
      <Table.Td><Text size="sm"><WorkspaceTime value={row.lastActivityAt} fallback="Not recorded" /></Text></Table.Td>
      <Table.Td>
        {row.services?.includes('code-server') ? (
          <Tooltip label={openCodeServerLabel} events={{ hover: true, focus: true, touch: false }}>
            <ActionIcon variant="subtle" size={32} aria-label={openCodeServerLabel} disabled={openingService}
              onClick={() => void openEntry('ide')}>
              <Image src="/services/vscode-icon.png" alt="" w={26} h={26} />
            </ActionIcon>
          </Tooltip>
        ) : null}
      </Table.Td>
      <Table.Td><Stack gap={4} align="flex-start">
        <Menu opened={opened} onChange={setOpened} position="bottom-end" withinPortal transitionProps={{ duration: 0 }}>
          <Menu.Target><Button ref={trigger} variant="default" size="compact-sm" aria-label={`Actions for ${name}`}>Actions</Button></Menu.Target>
          <Menu.Dropdown mah="min(24rem, 65vh)" style={{ overflowY: 'auto' }}>
            {detail.isFetching ? <Menu.Label>Checking available actions…</Menu.Label> : detail.isError ? (
              <Menu.Item closeMenuOnClick={false} onClick={() => void detail.refetch()}>Retry loading actions</Menu.Item>
            ) : workspace ? <>
              {commands.interrupt ? <Menu.Item disabled={!!command.submitting} onClick={() => void command.run('stop')}>
                Stop deployment
              </Menu.Item> : null}
              {lifecycle && !commands.interrupt ? <Menu.Item disabled={busy} onClick={() => void command.run(lifecycle)}>
                {lifecycle === 'start' ? 'Start' : 'Stop'}
              </Menu.Item> : null}
              {commands.rebuild ? <Menu.Item disabled={busy} onClick={() => void command.run('rebuild')}>Rebuild</Menu.Item> : null}
              {workspace.canOpenIde ? <Menu.Item disabled={openingService} onClick={() => void openEntry('ide')}>Open IDE</Menu.Item> : null}
              {workspace.canOpenRepair ? <Menu.Item disabled={openingService} onClick={() => void openEntry('repair')}>Open repair IDE</Menu.Item> : null}
              {commands.lifecycle && workspace.canStartRepair ? <Menu.Item disabled={busy} onClick={() => void command.run('repair')}>Start repair</Menu.Item> : null}
              {commands.lifecycle && workspace.canRetryAfterRepair ? <Menu.Item disabled={busy} onClick={() => void command.run('retry')}>Exit repair and retry setup</Menu.Item> : null}
              {commands.update ? <Menu.Item disabled={busy} onClick={() => void command.run('update')}>Update now</Menu.Item> : null}
              {workspace.canOpenPreviews ? (workspace.previewPorts ?? []).map(port => (
                <Menu.Item key={port} disabled={openingService} onClick={() => void openEntry(`preview-${port}`)}>Open preview {port}</Menu.Item>
              )) : null}
            </> : null}
            <Menu.Item component="a" href={href}>Open workspace</Menu.Item>
            {progressID ? <Menu.Item onClick={() => { setOperationID(progressID); setDialog('progress'); }}>View progress</Menu.Item> : null}
            {commands.delete && !detail.isFetching ? <>
              <Menu.Divider />
              <Menu.Item color="red" disabled={busy} onClick={() => { command.setError(undefined); setDialog('delete'); }}>
                Delete workspace
              </Menu.Item>
            </> : null}
          </Menu.Dropdown>
        </Menu>
        {command.error !== undefined && dialog !== 'delete' ? <APIProblemView error={command.error} /> : null}
        {entryError === 'popup-blocked' ? (
          <Alert color="red" title="Browser tab blocked">Allow pop-ups for ShiftBox and try again.</Alert>
        ) : entryError !== undefined ? <APIProblemView error={entryError} /> : null}
        <DeleteWorkspaceDialog name={workspace?.name || name} opened={dialog === 'delete'} busy={busy || !commands.delete}
          submitting={command.submitting === 'delete'} error={command.error} cancel={close} confirm={() => void command.run('delete')} />
        {operationID ? (
          <Modal opened={dialog === 'progress'} onClose={close} title="Workspace operation" transitionProps={{ duration: 0 }}>
            <WorkspaceProgress key={operationID} workspaceId={row.id} operationId={operationID} />
          </Modal>
        ) : null}
      </Stack></Table.Td>
    </Table.Tr>
  );
}
