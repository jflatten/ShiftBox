'use client';

import { useState } from 'react';
import { Alert, Button, Checkbox, Stack, Text, Title } from '@mantine/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { WorkflowStepper, type WorkflowOperation } from '../../components/workflow-stepper';
import { readProblem } from '../../lib/api-problem';
import { SignInChoices } from '../session/sign-in';

interface Review {
  id: string;
  version: number;
  state: 'identity_review' | 'provider_reconcile' | 'blocked' | 'authorization_review' | 'ready' | 'activated';
  sections: {
    identities?: { id: string; primaryEmail: string; role: string; membershipState: string; credentialMode: string }[];
    workspaces?: { workspaceId: string; match: string; power?: string }[];
  };
}

const steps = [
  { key: 'identity_review', label: 'Review identities' },
  { key: 'provider_reconcile', label: 'Reconcile provider inventory' },
  { key: 'authorization_review', label: 'Review authorization and retention' },
  { key: 'ready', label: 'Activate the installation' },
];
const matchLabels: Record<string, string> = { matched: 'Matched', missing: 'Missing', ownership_conflict: 'Ownership conflict', unverified: 'Unverified' };

export function RecoveryReview({ csrfToken, reviewId }: { csrfToken: string; reviewId: string }) {
  const endpoint = `/api/v1/recovery/${encodeURIComponent(reviewId)}`;
  const client = useQueryClient();
  const queryKey = ['recovery-review', reviewId];
  const query = useQuery<Review>({
    queryKey, gcTime: 0, retry: false,
    queryFn: async ({ signal }) => {
      const response = await fetch(endpoint, { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
  const [checked, setChecked] = useState<string[]>([]);
  const [retention, setRetention] = useState(false);
  const [busy, setBusy] = useState(false);
  const [activated, setActivated] = useState<WorkflowOperation>();
  const [error, setError] = useState<unknown>();
  const review = query.isError ? undefined : query.data;

  async function decide(section: string, decisions: object) {
    if (!review || busy) return;
    setBusy(true);
    setError(undefined);
    try {
      const response = await fetch(endpoint, {
        method: 'PATCH', cache: 'no-store',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'If-Match': `"${review.version}"` },
        body: JSON.stringify({ section, decisions }),
      });
      if (!response.ok) {
        const problem = await readProblem(response);
        if ([401, 403, 409, 412].includes(response.status)) await query.refetch();
        throw problem;
      }
      client.setQueryData(queryKey, await response.json());
      setRetention(false);
    } catch (error) {
      setError(error);
    } finally {
      setBusy(false);
    }
  }

  async function activate() {
    if (review?.state !== 'ready' || busy) return;
    setBusy(true);
    setError(undefined);
    try {
      const response = await fetch(`${endpoint}/activate`, { method: 'POST', cache: 'no-store',
        headers: { 'X-CSRF-Token': csrfToken, 'Idempotency-Key': crypto.randomUUID() } });
      if (!response.ok) {
        const problem = await readProblem(response);
        if ([401, 403, 409].includes(response.status)) await query.refetch();
        throw problem;
      }
      setActivated(await response.json());
    } catch (error) {
      setError(error);
    } finally {
      setBusy(false);
    }
  }

  if (activated) return <Stack>
    <WorkflowStepper operation={activated} />
    <Text>Recovery is complete. Sign in again to continue.</Text>
    <SignInChoices />
  </Stack>;
  if (!review) return query.isError
    ? <Stack align="flex-start"><APIProblemView error={query.error} /><Button variant="default" onClick={() => void query.refetch()}>Retry review</Button></Stack>
    : <Text role="status">Loading recovery review…</Text>;

  const identities = review.sections.identities ?? [];
  const workspaces = review.sections.workspaces ?? [];
  const blocked = review.state === 'blocked';
  const state = blocked ? 'provider_reconcile' : review.state;
  const active = steps.findIndex(step => step.key === state);
  const operation: WorkflowOperation = {
    id: review.id, currentStep: state,
    status: state === 'activated' ? 'succeeded' : blocked ? 'failed' : 'waiting',
    steps: steps.map((step, index) => ({
      ...step, index,
      status: state === 'activated' || index < active ? 'succeeded' : index === active
        ? blocked ? 'failed' : 'active' : 'pending',
    })),
  };
  return <Stack align="flex-start">
    <Title order={2}>Recovery review</Title>
    <Text>Workspace entry and destructive operations stay paused until activation. Restored sessions and grants must be issued again.</Text>
    <WorkflowStepper operation={operation} />
    {error !== undefined ? <APIProblemView error={error} /> : null}
    {state === 'identity_review' ? <Stack gap="sm">
      {identities.map(person => <Checkbox key={person.id} label={person.primaryEmail}
        description={`${person.role} · ${person.membershipState} · ${person.credentialMode}`}
        checked={checked.includes(person.id)} disabled={busy}
        onChange={event => {
          const selected = event.currentTarget.checked;
          setChecked(values => selected ? [...values, person.id] : values.filter(id => id !== person.id));
        }} />)}
      <Button disabled={identities.length === 0 || !identities.every(person => checked.includes(person.id))} loading={busy}
        onClick={() => void decide('identities', { userIds: identities.map(person => person.id) })}>Confirm identities</Button>
    </Stack> : null}
    {state === 'provider_reconcile' || state === 'authorization_review' || state === 'ready' ? <Stack gap="xs">
      {blocked ? <Alert color="red" title="Provider inventory needs attention">Resolve the listed workspace conditions on the controller host, then check the inventory again.</Alert> : null}
      {workspaces.map(workspace => <Text key={workspace.workspaceId} size="sm">
        {workspace.workspaceId} · {matchLabels[workspace.match] ?? 'Unverified'}{workspace.power ? ` · ${workspace.power}` : ''}
      </Text>)}
      {state === 'provider_reconcile' || state === 'ready' ? <Button variant={state === 'ready' ? 'default' : 'filled'}
        loading={busy} onClick={() => void decide('provider', {})}>Check provider inventory</Button> : null}
    </Stack> : null}
    {state === 'authorization_review' ? <Stack gap="sm">
      <Checkbox label="I reviewed authorization and retention settings and approve resuming the configured retention policy."
        checked={retention} disabled={busy} onChange={event => setRetention(event.currentTarget.checked)} />
      <Button disabled={!retention} loading={busy} onClick={() => void decide('authorization_retention', { acceptRetention: true })}>
        Confirm authorization and retention
      </Button>
    </Stack> : null}
    {state === 'ready' ? <Button loading={busy} onClick={() => void activate()}>Activate installation</Button> : null}
  </Stack>;
}
