import { useEffect, useId, useRef, useState } from 'react';
import { Alert, Anchor, Button, Checkbox, Group, Modal, Paper, SimpleGrid, Stack, Text, TextInput, Title } from '@mantine/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { WorkflowStepper } from '../../components/workflow-stepper';
import { readProblem } from '../../lib/api-problem';
import { useOperation } from '../../lib/use-operation';
import { workspaceName, workspaceState, type Workspace } from './workspace';
import { WorkspaceStatus, WorkspaceTime } from './workspace-summary';
import { RetentionWarning } from './retention';
import { WorkspaceConnectionGrants } from './connection-grants';

type WorkspaceAction = 'start' | 'stop' | 'rebuild' | 'repair' | 'retry' | 'delete' | 'update';

export function WorkspaceDetail({ id, csrfToken, admin }: { id: string; csrfToken: string; admin: boolean }) {
  const workspace = useQuery<Workspace>({
    queryKey: ['workspaces', 'detail', id],
    refetchInterval: query => query.state.data?.desiredState === 'deleted' ? false : 30_000,
    queryFn: async ({ signal }) => {
      const response = await fetch('/api/v1/workspaces/' + encodeURIComponent(id), { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
  const data = !workspace.isError ? workspace.data : undefined;
  const repairDescriptionId = useId();
  const [operationId, setOperationId] = useState<string>();
  const [operationBusy, setOperationBusy] = useState(false);
  const [submitting, setSubmitting] = useState<WorkspaceAction>();
  const [error, setError] = useState<unknown>();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const request = useRef<{ action: WorkspaceAction; version: number; key: string }>(undefined);
  const progressHeading = useRef<HTMLHeadingElement>(null);
  const progressFocus = useRef<{ id: string; trigger: Element | null }>(undefined);
  const displayedOperation = data?.currentOperationId ?? (operationBusy ? operationId : data?.lastFailedOperationId ?? operationId);
  const action = data?.desiredState === 'running' && data.observedState === 'running' ? 'stop' :
    data?.desiredState === 'stopped' && data.observedState === 'stopped' ? 'start' : undefined;
  const busy = !!data?.currentOperationId || operationBusy || !!submitting;
  const maintenance = data?.maintenance;

  useEffect(() => {
    const handoff = progressFocus.current;
    if (!handoff || handoff.id !== displayedOperation || !progressHeading.current) return;
    progressFocus.current = undefined;
    // The submitted action becomes disabled. Keep the next Tab in its
    // workflow unless the user already moved to another control.
    if (document.activeElement === document.body || document.activeElement === handoff.trigger) {
      progressHeading.current.focus();
    }
  }, [displayedOperation]);

  const requestAction = async (action: WorkspaceAction) => {
    if (!data || submitting) return;
    const trigger = document.activeElement;
    setSubmitting(action);
    setError(undefined);
    try {
      if (request.current?.action !== action || request.current.version !== data.version) {
        request.current = { action, version: data.version, key: crypto.randomUUID() };
      }
      const response = await fetch(`/api/v1/workspaces/${encodeURIComponent(id)}/actions/${action}`, {
        method: 'POST',
        headers: {
          'X-CSRF-Token': csrfToken, 'If-Match': `"${data.version}"`, 'Idempotency-Key': request.current.key,
          ...(action === 'delete' ? { 'Content-Type': 'application/json' } : {}),
        },
        body: action === 'delete' ? JSON.stringify({ confirmDataLoss: true }) : undefined,
      });
      if (!response.ok) {
        if (response.status === 412) void workspace.refetch();
        throw await readProblem(response);
      }
      const accepted: { id: string } = await response.json();
      if (action !== 'delete') progressFocus.current = { id: accepted.id, trigger };
      setOperationBusy(true);
      setOperationId(accepted.id);
      if (action === 'delete') setConfirmDelete(false);
      request.current = undefined;
      await workspace.refetch();
    } catch (error) {
      setError(error);
    } finally {
      setSubmitting(undefined);
    }
  };
  return (
    <>
      <Anchor href="/workspaces/">All workspaces</Anchor>
      <Group justify="space-between" align="flex-start">
        <Stack gap={4} style={{ minWidth: 0 }}>
          <Title order={1} style={{ overflowWrap: 'anywhere' }}>{data ? workspaceName(data) : 'Workspace'}</Title>
          <Text size="sm" c="dimmed">Manage your environment, connections, and retained data.</Text>
        </Stack>
        <Button variant="default" loading={workspace.isFetching} onClick={() => void workspace.refetch()}>Refresh</Button>
      </Group>
      {workspace.isError ? <Stack align="flex-start">
        <APIProblemView error={workspace.error} />
        <Button variant="default" onClick={() => void workspace.refetch()}>Retry</Button>
      </Stack> : null}
      {workspace.isPending ? <Text role="status">Loading workspace…</Text> : null}
      {data ? (
        <Stack gap="lg">
          <Paper component="section" aria-label="Workspace controls" withBorder radius="md" p="md">
            <Stack gap="md">
              <Group gap="xl">
                <Stack gap={4} align="flex-start"><Text size="sm" c="dimmed">Observed state</Text><WorkspaceStatus value={data.observedState} /></Stack>
                <Stack gap={4}><Text size="sm" c="dimmed">Desired state</Text><Text fw={600}>{workspaceState(data.desiredState)}</Text></Stack>
              </Group>
              <Group align="flex-start">
                {data.canOpenIde ? <WorkspaceEntryButton id={id} csrfToken={csrfToken} /> : null}
                {data.canOpenRepair ? <WorkspaceEntryButton id={id} csrfToken={csrfToken} service="repair" describedBy={repairDescriptionId} /> : null}
                {action ? (
                  <Group>
                    <Button variant={data.canOpenIde ? "default" : "filled"} loading={submitting === action} disabled={!!data.currentOperationId || operationBusy || !!submitting}
                      onClick={() => void requestAction(action)}>
                      {action === 'start' ? 'Start workspace' : 'Stop workspace'}
                    </Button>
                    {action === 'stop' && !data.canOpenRepair && !data.canRetryAfterRepair ? (
                      <Button variant="default" loading={submitting === 'rebuild'} disabled={!!data.currentOperationId || operationBusy || !!submitting}
                        onClick={() => void requestAction('rebuild')}>
                        Rebuild container
                      </Button>
                    ) : null}
                    {data.canStartRepair ? (
                      <Button variant="default" loading={submitting === 'repair'} disabled={!!data.currentOperationId || operationBusy || !!submitting}
                        aria-describedby={repairDescriptionId} onClick={() => void requestAction('repair')}>
                        Start repair
                      </Button>
                    ) : null}
                    {data.canRetryAfterRepair ? (
                      <Button variant="default" loading={submitting === 'retry'} disabled={!!data.currentOperationId || operationBusy || !!submitting}
                        onClick={() => void requestAction('retry')}>
                        Exit repair and retry setup
                      </Button>
                    ) : null}
                  </Group>
                ) : null}
              </Group>
              {error !== undefined && !confirmDelete ? <APIProblemView error={error} /> : null}
              <Text size="sm">Stop and start preserve project files, the developer home, and named application volumes. Rebuilding also preserves this data and replaces the container’s writable layer.</Text>
              {data.canStartRepair || data.canOpenRepair || data.canRetryAfterRepair ? (
                <Text id={repairDescriptionId} size="sm">Repair opens your project files in a separate IDE and skips project hooks, tasks, and extensions.</Text>
              ) : null}
            </Stack>
          </Paper>
          <RetentionWarning workspace={data} />
          {maintenance ? <Alert title="Scheduled maintenance" color={maintenance.state === 'failed' ? 'red' : 'yellow'}>
            <Stack align="flex-start" gap="xs">
              <Text size="sm">Guest release {maintenance.releaseId} · Deadline <WorkspaceTime value={maintenance.deadline} fallback="Unavailable" /></Text>
              {action && !maintenance.operationId && (maintenance.state === 'pending' || maintenance.state === 'waiting_safe') ? (
                <Button variant="default" loading={submitting === 'update'} disabled={busy} onClick={() => void requestAction('update')}>
                  Update now
                </Button>
              ) : null}
            </Stack>
          </Alert> : null}
          {displayedOperation ? <Stack gap="xs">
            <Title order={2} size="h4" tabIndex={-1} ref={progressHeading}>Workspace operation</Title>
            <WorkspaceProgress key={displayedOperation} workspaceId={id} operationId={displayedOperation} onFinished={setOperationBusy} />
          </Stack> : null}
          <Paper component="section" aria-label="Workspace configuration" withBorder radius="md" p="md">
            <Stack gap="md">
              <Title order={2} size="h3">Configuration</Title>
              <Text fw={600}>{data.resources.cpuCount} vCPU · {data.resources.memoryMiB} MiB memory · {data.resources.diskMiB} MiB disk</Text>
              <SimpleGrid component="dl" cols={{ base: 1, sm: 2 }} spacing="md" m={0}>
                <div>
                  <Text component="dt" size="sm" c="dimmed">Template revision</Text>
                  <Text component="dd" m={0}>Revision {data.templateRevision}</Text>
                </div>
                <div>
                  <Text component="dt" size="sm" c="dimmed">Platform release</Text>
                  <Text component="dd" m={0}>{data.platformRelease}</Text>
                </div>
                <div>
                  <Text component="dt" size="sm" c="dimmed">Last activity</Text>
                  <Text component="dd" m={0}><WorkspaceTime value={data.lastActivityAt} fallback="No activity recorded" /></Text>
                </div>
                <div>
                  <Text component="dt" size="sm" c="dimmed">Retention deadline</Text>
                  <Text component="dd" m={0}><WorkspaceTime value={data.retentionDeadline} fallback="No deadline scheduled" /></Text>
                </div>
              </SimpleGrid>
            </Stack>
          </Paper>
          {data.canOpenPreviews || data.canEditPreviewPorts ? (
            <Paper component="section" aria-label="Previews" withBorder radius="md" p="md">
              <Stack gap="md">
                <Title order={2} size="h3">Previews</Title>
                <WorkspacePreviews key={(data.previewPorts ?? []).join(',')} workspace={data} csrfToken={csrfToken}
                  disabled={busy || data.desiredState === 'deleted'} refresh={workspace.refetch} />
              </Stack>
            </Paper>
          ) : null}
          {admin ? <Paper withBorder radius="md" p="md"><WorkspaceConnectionGrants workspaceId={id} csrfToken={csrfToken} /></Paper> : null}
          {data.desiredState !== 'deleted' && data.observedState !== 'deleted' ? (
            <Paper component="section" aria-label="Delete workspace" withBorder radius="md" p="md">
              <Stack gap="sm" align="flex-start">
                <Title order={2} size="h3">Delete workspace</Title>
                <Text size="sm" c="dimmed">Permanently remove this environment and its files. Push work you need to keep before deleting.</Text>
                <Button variant="light" color="red" disabled={busy}
                  onClick={() => { setError(undefined); setConfirmDelete(true); }}>Delete workspace</Button>
              </Stack>
            </Paper>
          ) : null}
          <DeleteWorkspaceDialog name={data.name || id} opened={confirmDelete} busy={busy} submitting={submitting === 'delete'} error={error}
            cancel={() => setConfirmDelete(false)} confirm={() => void requestAction('delete')} />
        </Stack>
      ) : null}
    </>
  );
}

function DeleteWorkspaceDialog({ name, opened, busy, submitting, error, cancel, confirm }: {
  name: string;
  opened: boolean;
  busy: boolean;
  submitting: boolean;
  error: unknown;
  cancel: () => void;
  confirm: () => void;
}) {
  const [typedName, setTypedName] = useState('');
  const [accepted, setAccepted] = useState(false);
  const enabled = typedName === name && accepted && !busy;
  return (
    <Modal opened={opened} onClose={cancel} onExitTransitionEnd={() => { setTypedName(''); setAccepted(false); }}
      title="Delete workspace" closeOnClickOutside={false} closeOnEscape={!submitting} withCloseButton={!submitting}
      transitionProps={{ duration: 0 }}>
      <form onSubmit={event => { event.preventDefault(); if (enabled) confirm(); }}>
        <Stack>
          <Text>Permanently delete this workspace, including project files, the developer home, and named application volumes.</Text>
          <Text size="sm">Management backups exclude workspace VM disks.</Text>
          <Text size="sm">Workspace name <Text span fw={600}>{name}</Text></Text>
          <TextInput label="Type the workspace name" value={typedName} onChange={event => setTypedName(event.currentTarget.value)}
            disabled={submitting} autoComplete="off" />
          <Checkbox label="I have pushed work I need to keep." checked={accepted}
            onChange={event => setAccepted(event.currentTarget.checked)} disabled={submitting} />
          {error !== undefined ? <APIProblemView error={error} /> : null}
          <Group justify="flex-end">
            <Button type="button" variant="default" data-autofocus disabled={submitting} onClick={cancel}>Cancel</Button>
            <Button type="submit" color="red" disabled={!enabled} loading={submitting}>Delete permanently</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

function WorkspacePreviews({ workspace, csrfToken, disabled, refresh }: {
  workspace: Workspace; csrfToken: string; disabled: boolean; refresh: () => Promise<unknown>;
}) {
  const [value, setValue] = useState((workspace.previewPorts ?? []).join(', '));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>();
  const save = async () => {
    const values = value.trim() ? value.split(',').map(port => port.trim()) : [];
    const ports = values.map(Number);
    if (ports.length > 32 || new Set(ports).size !== ports.length ||
        values.some((port, index) => !/^\d{1,5}$/.test(port) || ports[index] < 1 || ports[index] > 65535)) {
      setError(new Error('Enter up to 32 distinct ports from 1 to 65535.'));
      return;
    }
    setSaving(true);
    setError(undefined);
    try {
      const response = await fetch(`/api/v1/workspaces/${encodeURIComponent(workspace.id)}`, {
        method: 'PATCH', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'If-Match': `"${workspace.version}"` },
        body: JSON.stringify({ previewPorts: ports }),
      });
      if (!response.ok) throw await readProblem(response);
      await refresh();
    } catch (err) {
      setError(err);
    } finally {
      setSaving(false);
    }
  };
  return (
    <Stack gap="xs">
      {workspace.canEditPreviewPorts ? <form onSubmit={event => { event.preventDefault(); void save(); }}>
        <Stack gap="xs" align="flex-start">
          <TextInput label="Preview ports" description="Comma-separated ports on the workspace VM." value={value}
            onChange={event => setValue(event.currentTarget.value)} disabled={disabled || saving} />
          <Button type="submit" variant="default" disabled={disabled} loading={saving}>Save preview ports</Button>
        </Stack>
      </form> : null}
      {error !== undefined ? <APIProblemView error={error} /> : null}
      {workspace.canOpenPreviews ? <Group>
        {(workspace.previewPorts ?? []).map(port => <WorkspaceEntryButton key={port} id={workspace.id} csrfToken={csrfToken} service={`preview-${port}`} />)}
      </Group> : null}
    </Stack>
  );
}

function WorkspaceEntryButton({ id, csrfToken, service = 'ide', describedBy }: {
  id: string; csrfToken: string; service?: 'ide' | 'repair' | `preview-${number}`; describedBy?: string;
}) {
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<unknown>();

  const open = async () => {
    if (submitting) return;
    setSubmitting(true);
    setError(undefined);
    let tab: Window | null = null;
    try {
      // Reserve the tab during the click, before waiting for the grant.
      tab = window.open('about:blank', '_blank');
      if (!tab) {
        setError('popup-blocked');
        return;
      }
      tab.opener = null;
      const response = await fetch(`/api/v1/workspaces/${encodeURIComponent(id)}/entry-grants`, {
        method: 'POST', cache: 'no-store',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
        body: JSON.stringify({ service }),
      });
      if (!response.ok) throw await readProblem(response);
      const { exchangeUrl } = await response.json();
      const url = new URL(exchangeUrl);
      if (url.protocol !== 'https:' || url.origin === window.location.origin || url.pathname !== '/_shiftbox/entry' ||
          url.username || url.password || !url.searchParams.get('grant')) {
        throw new Error('Invalid workspace entry URL');
      }
      if (tab.closed) return;
      const entry = tab.document.createElement('a');
      entry.href = url.href;
      entry.rel = 'noreferrer';
      entry.click();
    } catch (error) {
      tab?.close();
      setError(error);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Stack align="flex-start" gap="xs">
      <Button loading={submitting} aria-describedby={describedBy} onClick={() => void open()}>
        {service === 'ide' ? 'Open IDE' : service === 'repair' ? 'Open repair IDE' : `Open preview ${service.slice(8)}`}
      </Button>
      {error === 'popup-blocked' ? (
        <Alert color="red" title="Browser tab blocked">Allow pop-ups for ShiftBox and try again.</Alert>
      ) : error !== undefined ? <APIProblemView error={error} /> : null}
    </Stack>
  );
}

function WorkspaceProgress({ workspaceId, operationId, onFinished }: { workspaceId: string; operationId: string; onFinished: (busy: boolean) => void }) {
  const operation = useOperation(operationId);
  const client = useQueryClient();
  const state = operation.data?.status;
  const ideVerified = operation.data?.steps.some(step => step.key === 'verify_ide' && step.status === 'succeeded');
  useEffect(() => {
    const finished = state === 'succeeded' || state === 'failed' || state === 'cancelled';
    if (finished) onFinished(false);
    if (finished || ideVerified) void client.invalidateQueries({ queryKey: ['workspaces', 'detail', workspaceId] });
  }, [client, ideVerified, onFinished, state, workspaceId]);
  if (operation.accessDenied) return <Text role="alert">You no longer have access to this operation.</Text>;
  if (operation.isError) return <APIProblemView error={operation.error} />;
  if (!operation.data) return <Text role="status">Loading operation…</Text>;
  return <WorkflowStepper operation={operation.data} />;
}
