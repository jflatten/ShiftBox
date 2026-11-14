'use client';

import { useState } from 'react';
import { Button, NativeSelect, Stack, Table, Text, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { ShiftBoxShell } from '../../components/app-shell';
import { APIProblemView } from '../../components/api-problem';
import { APIProblemError, readProblem } from '../../lib/api-problem';
import { useSession } from '../../lib/use-session';

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
  return (
    <ShiftBoxShell session={current} alerts={[]} error={session.error ?? (allowed ? people.error : undefined) ?? undefined}>
      <Title order={1}>People</Title>
      {session.isPending ? <Text role="status">Checking your session…</Text> : null}
      {session.data === null ? <Button component="a" href="/auth/login" style={{ alignSelf: 'flex-start' }}>Sign in</Button> : null}
      {current && !allowed ? <Text>Administrator access required.</Text> : null}
      {allowed && people.isPending ? <Text role="status">Loading people…</Text> : null}
      {allowed && people.isError ? (
        <Button variant="default" style={{ alignSelf: 'flex-start' }} onClick={() => void people.refetch()}>Retry</Button>
      ) : null}
      {data?.length === 0 ? <Text>No accounts found.</Text> : null}
      {data?.length && current ? <PeopleTable people={data}
        approvalToken={current.permissions.includes('user.approve') ? current.csrfToken : undefined}
        refresh={people.refetch} /> : null}
    </ShiftBoxShell>
  );
}

function PeopleTable({ people, approvalToken, refresh }: {
  people: Person[];
  approvalToken?: string;
  refresh: () => Promise<unknown>;
}) {
  return (
    <Table.ScrollContainer minWidth={520} type="native">
      <Table aria-label="Organization membership">
        <Table.Thead>
          <Table.Tr>
            <Table.Th scope="col">Account</Table.Th>
            <Table.Th scope="col">Role</Table.Th>
            <Table.Th scope="col">Membership</Table.Th>
            <Table.Th scope="col">Sign-in</Table.Th>
            {approvalToken ? <Table.Th scope="col">Approval</Table.Th> : null}
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {people.map(person => (
            <Table.Tr key={person.id}>
              <Table.Th scope="row">
                {person.displayName ? <Text fw={600}>{person.displayName}</Text> : null}
                <Text size="sm">{person.primaryEmail}</Text>
              </Table.Th>
              <Table.Td>{roleLabels[person.role]}</Table.Td>
              <Table.Td>{membershipLabels[person.membershipState]}</Table.Td>
              <Table.Td>{credentialLabels[person.credentialMode]}</Table.Td>
              {approvalToken ? (
                <Table.Td>{person.membershipState === 'pending' ? (
                  <ApprovePerson key={person.version} person={person} csrfToken={approvalToken} refresh={refresh} />
                ) : null}</Table.Td>
              ) : null}
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Table.ScrollContainer>
  );
}

function ApprovePerson({ person, csrfToken, refresh }: {
  person: Person;
  csrfToken: string;
  refresh: () => Promise<unknown>;
}) {
  const [role, setRole] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>();
  const stale = error instanceof APIProblemError && error.problem.status === 412;
  const approve = async () => {
    if (!role || saving) return;
    setSaving(true);
    setError(undefined);
    try {
      const response = await fetch(`/api/v1/admin/users/${encodeURIComponent(person.id)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'If-Match': `"${person.version}"` },
        body: JSON.stringify({ membershipState: 'active', role }),
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
      <Button size="compact-sm" disabled={!role} loading={saving} onClick={() => void approve()}>Approve</Button>
      {error !== undefined ? <APIProblemView error={error} /> : null}
      {stale ? <Button variant="default" size="compact-sm" onClick={() => void refresh()}>Refresh membership</Button> : null}
    </Stack>
  );
}
