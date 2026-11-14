'use client';

import { Button, Table, Text, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { ShiftBoxShell } from '../../components/app-shell';
import { readProblem } from '../../lib/api-problem';
import { useSession } from '../../lib/use-session';

interface Person {
  id: string;
  primaryEmail: string;
  displayName?: string | null;
  role: 'member' | 'administrator';
  membershipState: 'pending' | 'active' | 'disabled';
  credentialMode: 'local' | 'external';
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
      {data?.length ? <PeopleTable people={data} /> : null}
    </ShiftBoxShell>
  );
}

function PeopleTable({ people }: { people: Person[] }) {
  return (
    <Table.ScrollContainer minWidth={520} type="native">
      <Table aria-label="Organization membership">
        <Table.Thead>
          <Table.Tr>
            <Table.Th scope="col">Account</Table.Th>
            <Table.Th scope="col">Role</Table.Th>
            <Table.Th scope="col">Membership</Table.Th>
            <Table.Th scope="col">Sign-in</Table.Th>
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
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Table.ScrollContainer>
  );
}
