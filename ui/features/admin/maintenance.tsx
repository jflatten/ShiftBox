'use client';

import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Divider, Group, List, Stack, Text, TextInput, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { WorkflowStepper } from '../../components/workflow-stepper';
import { readProblem } from '../../lib/api-problem';
import { useOperation } from '../../lib/use-operation';
import type { components } from '../../lib/api-types';
import type { Capacity } from '../../lib/use-infrastructure';

const endpoint = '/api/v1/admin/maintenance';
const campaignQuery = ['maintenance-campaigns'];
type Campaign = components['schemas']['MaintenanceCampaign'];
type Workspace = components['schemas']['Workspace'];

export function Maintenance({ userId, csrfToken, capacity }: { userId: string; csrfToken: string; capacity?: Capacity }) {
  const workspaces = useQuery<Workspace[]>({
    queryKey: ['maintenance', 'retained-workspaces', userId],
    gcTime: 0,
    queryFn: async ({ signal }) => {
      const retained: Workspace[] = [];
      let cursor: string | null | undefined;
      do {
        const suffix = cursor ? '?cursor=' + encodeURIComponent(cursor) : '';
        const response = await fetch('/api/v1/workspaces' + suffix, { signal, cache: 'no-store' });
        if (!response.ok) throw await readProblem(response);
        const page: { items: Workspace[]; nextCursor?: string | null } = await response.json();
        retained.push(...page.items.filter(workspace => workspace.desiredState !== 'deleted' && workspace.observedState !== 'deleted'));
        cursor = page.nextCursor;
      } while (cursor);
      return retained;
    },
  });
  const campaigns = useQuery<Campaign[]>({
    queryKey: campaignQuery,
    refetchInterval: query => query.state.data?.some(campaign => campaign.state === 'scheduled' || campaign.state === 'active') ? 5000 : false,
    gcTime: 0,
    queryFn: async ({ signal }) => {
      const response = await fetch(endpoint, { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: { releaseId: '', deadline: '' },
    validate: {
      releaseId: value => value.trim() && value.trim().length <= 128 ? null : 'Enter a target release ID.',
      deadline: value => Date.parse(value) > Date.now() ? null : 'Choose a future deadline.',
    },
  });
  const [selected, setSelected] = useState<string>();
  const [error, setError] = useState<unknown>();
  const request = useRef<{ body: string; key: string }>(undefined);
  const canSchedule = workspaces.isSuccess && !!capacity;

  async function schedule(values: typeof form.values) {
    if (!canSchedule) return;
    setError(undefined);
    const body = JSON.stringify({ releaseId: values.releaseId.trim(), deadline: new Date(values.deadline).toISOString() });
    if (request.current?.body !== body) request.current = { body, key: crypto.randomUUID() };
    try {
      const response = await fetch(endpoint, {
        method: 'POST', cache: 'no-store', body,
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'Idempotency-Key': request.current.key },
      });
      if (!response.ok) throw await readProblem(response);
      const accepted: { id: string } = await response.json();
      request.current = undefined;
      setSelected(accepted.id);
      form.reset();
      await campaigns.refetch();
    } catch (error) {
      setError(error);
    }
  }

  return (
    <Stack component="section" gap="sm">
      <Title order={2} size="h3">Maintenance</Title>
      <Text size="sm">Schedule guest updates for retained workspaces. The deadline uses your browser’s local time.</Text>
      <Title order={3} size="h4" mt="sm">Affected workspaces</Title>
      {capacity ? <Text size="sm">Capacity budget · {capacity.cpuCount} vCPU · {capacity.memoryMiB} MiB memory · {capacity.diskMiB} MiB disk</Text> : null}
      {workspaces.isPending ? <Text role="status">Loading retained workspaces…</Text> : null}
      {workspaces.isError ? <Group align="flex-start">
        <APIProblemView error={workspaces.error} />
        <Button variant="default" onClick={() => void workspaces.refetch()}>Retry workspace review</Button>
      </Group> : null}
      {workspaces.isSuccess ? <>
        <Text size="sm">{workspaces.data.length} retained {workspaces.data.length === 1 ? 'workspace' : 'workspaces'}</Text>
        {workspaces.data.length ? <List size="sm">
          {workspaces.data.map(workspace => (
            <List.Item key={workspace.id}>
              {workspace.name && workspace.name !== workspace.id ? workspace.name : 'Unnamed workspace'}
              {' · '}{workspace.resources.cpuCount} vCPU · {workspace.resources.memoryMiB} MiB memory · {workspace.resources.diskMiB} MiB disk
            </List.Item>
          ))}
        </List> : null}
      </> : null}
      <Divider my="sm" />
      <Title order={3} size="h4">Schedule an update</Title>
      <form noValidate onSubmit={form.onSubmit(schedule, errors => form.getInputNode(Object.keys(errors)[0])?.focus())}>
        <Stack maw={420}>
          <TextInput label="Target release ID" required disabled={form.submitting}
            key={form.key('releaseId')} {...form.getInputProps('releaseId')} />
          <TextInput label="Deadline" type="datetime-local" required disabled={form.submitting}
            key={form.key('deadline')} {...form.getInputProps('deadline')} />
          {error !== undefined ? <APIProblemView error={error} /> : null}
          <Button type="submit" disabled={!canSchedule} loading={form.submitting} style={{ alignSelf: 'flex-start' }}>Schedule maintenance</Button>
        </Stack>
      </form>
      <Divider my="sm" />
      <Title order={3} size="h4">Update campaigns</Title>
      {campaigns.isError ? <Group align="flex-start">
        <APIProblemView error={campaigns.error} />
        <Button variant="default" onClick={() => void campaigns.refetch()}>Retry campaigns</Button>
      </Group> : null}
      {campaigns.isPending ? <Text role="status">Loading campaigns…</Text> : null}
      {campaigns.isSuccess && campaigns.data.length === 0 ? <Text size="sm" c="dimmed">No maintenance campaigns yet.</Text> : null}
      {!campaigns.isError && campaigns.data?.length ? <Stack component="ul" gap="xs" p={0} m={0} style={{ listStyle: 'none' }}>
        {campaigns.data.map(campaign => (
          <Group component="li" key={campaign.id} justify="space-between">
            <Text size="sm">{campaign.releaseId} · {campaign.state} · <time dateTime={campaign.deadline}>
              {new Date(campaign.deadline).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })}
            </time></Text>
            <Button variant="default" onClick={() => setSelected(campaign.operationId)}>View progress</Button>
          </Group>
        ))}
      </Stack> : null}
      {campaigns.data?.[0]?.state === 'blocked' && !campaigns.isError ? (
        <Alert title="Maintenance needs attention" color="orange">
          Review the failed operation, then schedule a replacement campaign or retire the affected workspace.
        </Alert>
      ) : null}
      {selected ? <CampaignProgress key={selected} id={selected} /> : null}
    </Stack>
  );
}

function CampaignProgress({ id }: { id: string }) {
  const client = useQueryClient();
  const progress = useOperation(id);
  const status = progress.data?.status;
  useEffect(() => {
    if (status === 'succeeded' || status === 'failed' || status === 'cancelled') {
      void client.invalidateQueries({ queryKey: campaignQuery, exact: true });
    }
  }, [client, status]);
  if (progress.accessDenied) return <Text role="alert">Maintenance progress is no longer available to this session.</Text>;
  if (progress.isError) return <Group>
    <Text role="alert">Maintenance progress is unavailable.</Text>
    <Button variant="default" onClick={() => void progress.refetch()}>Retry progress</Button>
  </Group>;
  return progress.data ? <WorkflowStepper operation={progress.data} /> : <Text role="status">Loading maintenance progress…</Text>;
}
