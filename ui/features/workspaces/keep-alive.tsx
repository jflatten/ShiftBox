import { useState } from 'react';
import { Button, Group, Select, Stack, Text } from '@mantine/core';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';
import { formatKeepAlive } from '../../lib/keep-alive';
import type { Workspace } from './workspace';
import { WorkspaceTime } from './workspace-summary';

export function WorkspaceKeepAlive({ workspace, csrfToken, disabled, refresh }: {
  workspace: Workspace; csrfToken: string; disabled: boolean; refresh: () => Promise<unknown>;
}) {
  const client = useQueryClient();
  const [draft, setDraft] = useState<string>();
  const [saved, setSaved] = useState(false);
  const policy = workspace.keepAlivePolicy;
  const override = workspace.keepAliveOverrideMinutes;
  const allowed = policy?.allowedMinutes.filter(value => policy.maximumMinutes === 0 || (value > 0 && value <= policy.maximumMinutes)) ?? [];
  const current = override != null && allowed.includes(override) ? String(override) : 'default';
  const selected = draft ?? current;
  const mutation = useMutation({
    mutationFn: async () => {
      const response = await fetch('/api/v1/workspaces/' + encodeURIComponent(workspace.id), {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'If-Match': `"${workspace.version}"` },
        body: JSON.stringify({ keepAliveMinutes: selected === 'default' ? null : Number(selected) }),
      });
      if (!response.ok) {
        const problem = await readProblem(response);
        if (response.status === 412) await refresh();
        throw problem;
      }
    },
    onSuccess: async () => {
      await refresh();
      await client.invalidateQueries({ queryKey: ['workspaces'] });
      setDraft(undefined);
      setSaved(true);
    },
  });
  if (!policy || workspace.keepAliveMinutes === undefined) return null;
  return <Stack gap="sm">
    <Text size="sm" fw={600}>Inactivity shutdown</Text>
    {workspace.canEditKeepAlive ? <Group align="flex-end">
      <Select label="Shutdown after inactivity" style={{ flex: '1 1 16rem', maxWidth: 400 }} allowDeselect={false}
        value={selected} disabled={disabled || mutation.isPending}
        data={[{ value: 'default', label: `Administrator default (${formatKeepAlive(policy.defaultMinutes)})` },
          ...allowed.map(value => ({ value: String(value), label: formatKeepAlive(value) }))]}
        onChange={value => { if (value !== null) { setDraft(value); setSaved(false); mutation.reset(); } }} />
      <Button variant="default" disabled={disabled || selected === current} loading={mutation.isPending}
        onClick={() => void mutation.mutate()}>Save keep-alive</Button>
    </Group> : <Text size="sm">Shutdown after inactivity: {formatKeepAlive(workspace.keepAliveMinutes)}</Text>}
    {workspace.keepAliveMinutes === 0
      ? <Text size="sm" c="dimmed">Unlimited — automatic inactivity shutdown is disabled.</Text>
      : <Text size="sm" c="dimmed">
        Stops after {formatKeepAlive(workspace.keepAliveMinutes)} of inactivity. IDE input, SSH commands and client input reset the timer.
        Idle SSH connections count toward inactivity. Use shiftbox watch &lt;PID&gt; to keep the workspace active while a process runs.
      </Text>}
    {workspace.inactivityShutdownAt ? <Text size="sm" c="dimmed">
      Inactive for {workspace.inactivityMinutes ?? 0} minutes · Stops at <WorkspaceTime value={workspace.inactivityShutdownAt} fallback="Unavailable" /> if no further activity is observed.
    </Text> : null}
    {mutation.isError ? <APIProblemView error={mutation.error} /> : null}
    {saved ? <Text size="sm" role="status">Keep-alive saved.</Text> : null}
  </Stack>;
}
