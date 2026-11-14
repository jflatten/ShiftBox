'use client';

import { useEffect, useRef, useState } from 'react';
import { Anchor, Button, NativeSelect, Stack, Table, Text, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { ShiftBoxShell } from '../../components/app-shell';
import { APIProblemView } from '../../components/api-problem';
import { APIProblemError, readProblem } from '../../lib/api-problem';
import { SignInChoices } from '../session/sign-in';
import { useSession } from '../../lib/use-session';
import { CreateLocalAccount } from './create-local-account';
import { Offboarding } from './offboarding';
import { ResetPassword } from './reset-password';

interface Person {
  id: string;
  primaryEmail: string;
  displayName?: string | null;
  role: 'member' | 'administrator';
  membershipState: 'pending' | 'active' | 'disabled';
  credentialMode: 'local' | 'external';
  version: number;
}

const roleLabels = { administrator: 'Administrator', member: 'Member' };
const membershipLabels = { pending: 'Pending', active: 'Active', disabled: 'Disabled' };
const credentialLabels = { local: 'Local', external: 'External' };

export function People() {
  const session = useSession();
  const current = session.session;
  const allowed = current?.role === 'administrator' && current.mode === 'normal' &&
    current.installationState === 'active' && current.permissions.includes('user.list');
  const people = useQuery<Person[]>({
    queryKey: ['people', current?.user.id],
    enabled: allowed,
    queryFn: async ({ signal }) => {
      const response = await fetch('/api/v1/admin/users', { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
  const data = allowed && !people.isError ? people.data : undefined;
  const [selected, setSelected] = useState<Person>();
  const [reset, setReset] = useState<Person>();
  const actionButton = useRef<HTMLButtonElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    if (selected || reset || !actionButton.current) return;
    // Disabling an account can remove the trigger from the updated table.
    const destination = actionButton.current.isConnected ? actionButton.current : heading.current;
    actionButton.current = null;
    destination?.focus();
  }, [reset, selected]);
  return (
    <ShiftBoxShell session={current} alerts={[]} error={session.error ?? (allowed ? people.error : undefined) ?? undefined}>
      <Title order={1} ref={heading} tabIndex={-1}>People</Title>
      {session.isPending ? <Text role="status">Checking your session…</Text> : null}
      {session.data === null ? <SignInChoices /> : null}
      {current && !allowed ? <Text>Administrator access required.</Text> : null}
      {allowed && people.isPending ? <Text role="status">Loading people…</Text> : null}
      {allowed && people.isError ? (
        <Button variant="default" style={{ alignSelf: 'flex-start' }} onClick={() => void people.refetch()}>Retry</Button>
      ) : null}
      {allowed && current.permissions.includes('user.create') ? (
        <CreateLocalAccount key={current.user.id} csrfToken={current.csrfToken} refresh={people.refetch} />
      ) : null}
      {data?.length === 0 ? <Text>No accounts found.</Text> : null}
      {data?.length && current ? <PeopleTable people={data}
        csrfToken={current.csrfToken}
        canApprove={current.permissions.includes('user.approve')}
        canChangeRole={current.permissions.includes('user.change_role')}
        onDisable={current.permissions.includes('user.disable') ? (person, trigger) => {
          actionButton.current = trigger;
          setSelected(person);
        } : undefined}
        onReset={current.permissions.includes('user.reset_password') ? (person, trigger) => {
          actionButton.current = trigger;
          setReset(person);
        } : undefined}
        refresh={people.refetch} /> : null}
      {selected && allowed && current?.permissions.includes('user.disable') ? (
        <Offboarding person={selected} csrfToken={current.csrfToken}
          onClose={() => setSelected(undefined)} refresh={people.refetch} />
      ) : null}
      {reset && allowed && current.permissions.includes('user.reset_password') ? (
        <ResetPassword person={reset} csrfToken={current.csrfToken} onClose={() => setReset(undefined)}
          refresh={reset.id === current.user.id ? session.refetch : people.refetch} />
      ) : null}
    </ShiftBoxShell>
  );
}

function PeopleTable({ people, csrfToken, canApprove, canChangeRole, onDisable, onReset, refresh }: {
  people: Person[];
  csrfToken: string;
  canApprove: boolean;
  canChangeRole: boolean;
  onDisable?: (person: Person, trigger: HTMLButtonElement) => void;
  onReset?: (person: Person, trigger: HTMLButtonElement) => void;
  refresh: () => Promise<unknown>;
}) {
  const hasActions = canApprove || canChangeRole || !!onDisable || !!onReset;
  return (
    <Table.ScrollContainer minWidth={520} type="native">
      <Table aria-label="Organization membership">
        <Table.Thead>
          <Table.Tr>
            <Table.Th scope="col">Account</Table.Th>
            <Table.Th scope="col">Role</Table.Th>
            <Table.Th scope="col">Membership</Table.Th>
            <Table.Th scope="col">Sign-in</Table.Th>
            {hasActions ? <Table.Th scope="col">Account actions</Table.Th> : null}
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {people.map(person => (
            <Table.Tr key={person.id}>
              <Table.Th scope="row">
                {person.displayName ? <Text fw={600}>{person.displayName}</Text> : null}
                <Text size="sm">{person.primaryEmail}</Text>
                {person.membershipState === 'disabled' ? (
                  <Anchor size="sm" href={`/workspaces/?owner=${encodeURIComponent(person.id)}`}
                    aria-label={`View workspaces for ${person.primaryEmail}`}>View workspaces</Anchor>
                ) : null}
              </Table.Th>
              <Table.Td>{roleLabels[person.role]}</Table.Td>
              <Table.Td>{membershipLabels[person.membershipState]}</Table.Td>
              <Table.Td>{credentialLabels[person.credentialMode]}</Table.Td>
              {hasActions ? (
                <Table.Td><Stack gap="xs" align="flex-start">{(canApprove && person.membershipState === 'pending') ||
                  (canChangeRole && person.membershipState === 'active') ? (
                  <MembershipRoleForm key={person.version} person={person} csrfToken={csrfToken} refresh={refresh} />
                ) : null}
                {onReset && person.membershipState === 'active' && person.credentialMode === 'local' ? (
                  <Button size="compact-sm" variant="subtle" onClick={event => onReset(person, event.currentTarget)}>Reset password</Button>
                ) : null}
                {onDisable && person.membershipState !== 'disabled' ? (
                  <Button size="compact-sm" color="red" variant="subtle" onClick={event => onDisable(person, event.currentTarget)}>Disable account</Button>
                ) : null}</Stack></Table.Td>
              ) : null}
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Table.ScrollContainer>
  );
}

function MembershipRoleForm({ person, csrfToken, refresh }: {
  person: Person;
  csrfToken: string;
  refresh: () => Promise<unknown>;
}) {
  const [role, setRole] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>();
  const stale = error instanceof APIProblemError && error.problem.status === 412;
  const approving = person.membershipState === 'pending';
  const canSave = !!role && (approving || role !== person.role);
  const save = async () => {
    if (!canSave || saving || stale) return;
    setSaving(true);
    setError(undefined);
    try {
      const response = await fetch(`/api/v1/admin/users/${encodeURIComponent(person.id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'If-Match': `"${person.version}"` },
        body: JSON.stringify(approving ? { membershipState: 'active', role } : { role }),
      });
      if (!response.ok) {
        const problem = await readProblem(response);
        if (response.status === 412) setRole('');
        throw problem;
      }
      await refresh();
    } catch (error) {
      setError(error);
    } finally {
      setSaving(false);
    }
  };
  return (
    <Stack gap="xs" align="flex-start">
      <NativeSelect aria-label={`Role for ${person.primaryEmail}`} value={role} disabled={saving || stale}
        onChange={event => setRole(event.currentTarget.value)}
        data={[{ value: '', label: 'Choose role' }, { value: 'member', label: 'Member' },
          { value: 'administrator', label: 'Administrator' }]} />
      <Button size="compact-sm" disabled={!canSave || stale} loading={saving} onClick={() => void save()}>
        {approving ? 'Approve' : 'Save role'}
      </Button>
      {error !== undefined ? <APIProblemView error={error} /> : null}
      {stale ? <Button variant="default" size="compact-sm" onClick={() => void refresh()}>Refresh membership</Button> : null}
    </Stack>
  );
}
