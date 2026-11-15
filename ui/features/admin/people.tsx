'use client';

import { useEffect, useRef, useState } from 'react';
import { Anchor, Badge, Button, Group, Menu, Modal, NativeSelect, Pagination, Paper, Stack, Table, Text, TextInput, Title, UnstyledButton } from '@mantine/core';
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
  const [editing, setEditing] = useState<Person>();
  const actionButton = useRef<HTMLButtonElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    if (selected || reset || editing || !actionButton.current) return;
    // Disabling an account can remove the trigger from the updated table.
    const destination = actionButton.current.isConnected ? actionButton.current : heading.current;
    actionButton.current = null;
    destination?.focus();
  }, [reset, selected, editing]);
  return (
    <ShiftBoxShell session={current} alerts={[]} error={session.error ?? (allowed ? people.error : undefined) ?? undefined}>
      <Group justify="space-between" align="flex-start">
        <Stack gap={4}>
          <Title order={1} ref={heading} tabIndex={-1}>Users</Title>
          <Text c="dimmed" size="sm">Manage accounts, roles, and access to ShiftBox.</Text>
        </Stack>
        {allowed && current.permissions.includes('user.create') ? (
          <CreateLocalAccount key={current.user.id} csrfToken={current.csrfToken} refresh={people.refetch} />
        ) : null}
      </Group>
      {session.isPending ? <Text role="status">Checking your session…</Text> : null}
      {session.data === null ? <SignInChoices /> : null}
      {current && !allowed ? <Text>Administrator access required.</Text> : null}
      {allowed && people.isPending ? <Text role="status">Loading users…</Text> : null}
      {allowed && people.isError ? (
        <Button variant="default" style={{ alignSelf: 'flex-start' }} onClick={() => void people.refetch()}>Retry</Button>
      ) : null}
      {data && current ? <PeopleTable people={data}
        currentUserID={current.user.id}
        canApprove={current.permissions.includes('user.approve')}
        canChangeRole={current.permissions.includes('user.change_role')}
        onEdit={(person, trigger) => {
          actionButton.current = trigger;
          setEditing(person);
        }}
        onDisable={current.permissions.includes('user.disable') ? (person, trigger) => {
          actionButton.current = trigger;
          setSelected(person);
        } : undefined}
        onReset={current.permissions.includes('user.reset_password') ? (person, trigger) => {
          actionButton.current = trigger;
          setReset(person);
        } : undefined} /> : null}
      {editing && allowed && (editing.membershipState === 'pending'
        ? current.permissions.includes('user.approve') : current.permissions.includes('user.change_role')) ? (
        <MembershipRoleForm person={editing} csrfToken={current.csrfToken}
          refresh={people.refetch} onClose={() => setEditing(undefined)} />
      ) : null}
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

const sortColumns = [
  { key: 'account', label: 'Account' },
  { key: 'role', label: 'Role' },
  { key: 'membershipState', label: 'Status' },
  { key: 'credentialMode', label: 'Sign-in' },
] as const;
type SortColumn = typeof sortColumns[number]['key'];

function sortValue(person: Person, column: SortColumn) {
  switch (column) {
    case 'account': return person.displayName || person.primaryEmail;
    case 'role': return roleLabels[person.role];
    case 'membershipState': return membershipLabels[person.membershipState];
    case 'credentialMode': return credentialLabels[person.credentialMode];
  }
}

type PersonAction = (person: Person, trigger: HTMLButtonElement) => void;

function PeopleTable({ people, currentUserID, canApprove, canChangeRole, onEdit, onDisable, onReset }: {
  people: Person[];
  currentUserID: string;
  canApprove: boolean;
  canChangeRole: boolean;
  onEdit: PersonAction;
  onDisable?: PersonAction;
  onReset?: PersonAction;
}) {
  const [search, setSearch] = useState('');
  const [role, setRole] = useState('');
  const [status, setStatus] = useState('');
  const [sort, setSort] = useState<{ column: SortColumn; direction: 'ascending' | 'descending' }>({ column: 'account', direction: 'ascending' });
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(10);
  const hasActions = canApprove || canChangeRole || !!onDisable || !!onReset;
  const query = search.trim().toLocaleLowerCase();
  // The API returns the complete directory. Filter and sort before slicing so
  // people outside the current page remain discoverable.
  const matches = people.filter(person =>
    (!role || person.role === role) && (!status || person.membershipState === status) &&
    (!query || [person.displayName, person.primaryEmail].some(value => value?.toLocaleLowerCase().includes(query)))
  ).sort((a, b) => {
    const comparison = sortValue(a, sort.column).localeCompare(sortValue(b, sort.column), undefined, { numeric: true, sensitivity: 'base' }) ||
      a.primaryEmail.localeCompare(b.primaryEmail) || a.id.localeCompare(b.id);
    return sort.direction === 'ascending' ? comparison : -comparison;
  });
  const pageCount = Math.max(1, Math.ceil(matches.length / pageSize));
  // A refresh after approval/offboarding can remove the last matching row.
  const currentPage = Math.min(page, pageCount);
  const start = (currentPage - 1) * pageSize;
  const visible = matches.slice(start, start + pageSize);
  const filtered = !!search || !!role || !!status;
  return (
    <Paper withBorder radius="md" p="md">
      <Stack gap="md">
        <Group align="flex-end">
          <TextInput type="search" label="Search users" placeholder="Search by name or email"
            value={search} onChange={event => { setSearch(event.currentTarget.value); setPage(1); }}
            style={{ flex: '1 1 220px' }} />
          <NativeSelect label="Filter by role" value={role} onChange={event => { setRole(event.currentTarget.value); setPage(1); }}
            data={[{ value: '', label: 'All roles' }, { value: 'administrator', label: 'Administrator' }, { value: 'member', label: 'Member' }]} />
          <NativeSelect label="Filter by status" value={status} onChange={event => { setStatus(event.currentTarget.value); setPage(1); }}
            data={[{ value: '', label: 'All statuses' }, ...Object.entries(membershipLabels).map(([value, label]) => ({ value, label }))]} />
          {filtered ? <Button variant="subtle" onClick={() => { setSearch(''); setRole(''); setStatus(''); setPage(1); }}>Clear filters</Button> : null}
        </Group>
        <Table.ScrollContainer minWidth={600} type="native">
          <Table aria-label="Organization membership" highlightOnHover verticalSpacing="sm" horizontalSpacing="sm">
            <Table.Thead>
              <Table.Tr>
                {sortColumns.map(column => <Table.Th key={column.key} scope="col"
                  aria-sort={sort.column === column.key ? sort.direction : undefined}>
                  <UnstyledButton fw={600} w="100%" py={4} aria-label={`Sort by ${column.label}`} onClick={() => {
                    setSort({ column: column.key, direction: sort.column === column.key && sort.direction === 'ascending' ? 'descending' : 'ascending' });
                    setPage(1);
                  }}>
                    <Group gap={6} wrap="nowrap"><span>{column.label}</span><span aria-hidden="true">{sort.column === column.key ? sort.direction === 'ascending' ? '↑' : '↓' : '↕'}</span></Group>
                  </UnstyledButton>
                </Table.Th>)}
                {hasActions ? <Table.Th scope="col">Actions</Table.Th> : null}
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {visible.map(person => (
                <Table.Tr key={person.id}>
                  <Table.Th scope="row">
                    <Text size="sm" fw={600}>{person.displayName || person.primaryEmail}</Text>
                    {person.displayName ? <Text size="xs" c="dimmed">{person.primaryEmail}</Text> : null}
                    {person.id === currentUserID ? <Text size="xs" c="dimmed">You</Text> : null}
                    {person.membershipState === 'disabled' ? (
                      <Anchor size="sm" href={`/workspaces/?owner=${encodeURIComponent(person.id)}`}
                        aria-label={`View workspaces for ${person.primaryEmail}`}>View workspaces</Anchor>
                    ) : null}
                  </Table.Th>
                  <Table.Td>{roleLabels[person.role]}</Table.Td>
                  <Table.Td><Badge variant="light" color={person.membershipState === 'active' ? 'green' : person.membershipState === 'pending' ? 'yellow' : 'gray'}>
                    {membershipLabels[person.membershipState]}
                  </Badge></Table.Td>
                  <Table.Td>{credentialLabels[person.credentialMode]}</Table.Td>
                  {hasActions ? (
                    <Table.Td><PersonActions person={person}
                      onEdit={(person.membershipState === 'pending' && canApprove) || (person.membershipState === 'active' && canChangeRole) ? onEdit : undefined}
                      onReset={person.membershipState === 'active' && person.credentialMode === 'local' ? onReset : undefined}
                      onDisable={person.membershipState !== 'disabled' ? onDisable : undefined} /></Table.Td>
                  ) : null}
                </Table.Tr>
              ))}
              {visible.length === 0 ? <Table.Tr><Table.Td colSpan={hasActions ? 5 : 4} py="xl">
                <Text ta="center" c="dimmed">{people.length === 0 ? 'No accounts found.' : 'No users match your search or filters.'}</Text>
              </Table.Td></Table.Tr> : null}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
        <Group justify="space-between" align="flex-end">
          <Text size="sm" c="dimmed" role="status">Showing {matches.length ? start + 1 : 0}–{Math.min(start + pageSize, matches.length)} of {matches.length} {matches.length === 1 ? 'user' : 'users'}</Text>
          <Group align="flex-end">
            <NativeSelect label="Rows per page" size="xs" value={pageSize} data={['10', '25', '50']}
              onChange={event => { setPageSize(Number(event.currentTarget.value)); setPage(1); }} />
            <Pagination total={pageCount} value={currentPage} onChange={setPage} size="sm" siblings={0}
              getItemProps={value => ({ 'aria-label': `Page ${value}` })}
              getControlProps={control => ({ 'aria-label': `${control === 'previous' ? 'Previous' : control === 'next' ? 'Next' : control === 'first' ? 'First' : 'Last'} page` })} />
          </Group>
        </Group>
      </Stack>
    </Paper>
  );
}

function PersonActions({ person, onEdit, onReset, onDisable }: {
  person: Person; onEdit?: PersonAction; onReset?: PersonAction; onDisable?: PersonAction;
}) {
  const trigger = useRef<HTMLButtonElement>(null);
  if (!onEdit && !onReset && !onDisable) return <Text size="sm" c="dimmed">—</Text>;
  const run = (action: PersonAction) => { if (trigger.current) action(person, trigger.current); };
  return <Menu position="bottom-end" withinPortal transitionProps={{ duration: 0 }}>
    <Menu.Target><Button ref={trigger} size="xs" variant="default" aria-label={`Actions for ${person.primaryEmail}`}>Actions</Button></Menu.Target>
    <Menu.Dropdown>
      {onEdit ? <Menu.Item onClick={() => run(onEdit)}>{person.membershipState === 'pending' ? 'Approve account' : 'Change role'}</Menu.Item> : null}
      {onReset ? <Menu.Item onClick={() => run(onReset)}>Reset password</Menu.Item> : null}
      {onDisable ? <><Menu.Divider /><Menu.Item color="red" onClick={() => run(onDisable)}>Disable account</Menu.Item></> : null}
    </Menu.Dropdown>
  </Menu>;
}

function MembershipRoleForm({ person, csrfToken, refresh, onClose }: {
  person: Person;
  csrfToken: string;
  refresh: () => Promise<unknown>;
  onClose: () => void;
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
      onClose();
    } catch (error) {
      setError(error);
    } finally {
      setSaving(false);
    }
  };
  return (
    <Modal opened returnFocus={false} title={approving ? 'Approve account' : 'Change role'}
      onClose={saving ? () => {} : onClose} closeOnClickOutside={false} closeOnEscape={!saving} withCloseButton={!saving}
      transitionProps={{ duration: 0 }}>
      <Stack gap="md">
        <Text fw={600}>{person.primaryEmail}</Text>
        <Text size="sm" c="dimmed">{approving ? 'Choose a role to approve this account for access to ShiftBox.' : `Current role: ${roleLabels[person.role]}`}</Text>
        <NativeSelect label="Role" aria-label={`Role for ${person.primaryEmail}`} value={role} disabled={saving || stale}
          onChange={event => setRole(event.currentTarget.value)}
          data={[{ value: '', label: 'Choose role' }, { value: 'member', label: 'Member' },
            { value: 'administrator', label: 'Administrator' }]} />
        {error !== undefined ? <APIProblemView error={error} /> : null}
        {stale ? <Button variant="default" onClick={async () => { await refresh(); onClose(); }}>Refresh membership</Button> : null}
        <Group justify="flex-end">
          <Button variant="default" data-autofocus disabled={saving} onClick={onClose}>Cancel</Button>
          <Button disabled={!canSave || stale} loading={saving} onClick={() => void save()}>
            {approving ? 'Approve' : 'Save role'}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
