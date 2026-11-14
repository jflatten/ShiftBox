import { useState } from 'react';
import { Button, Group, Modal, Stack, Table, Text, Title } from '@mantine/core';
import { useMutation, useQuery } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';
import type { operations } from '../../lib/api-types';

type GrantList = operations['listWorkspaceConnectionGrants']['responses'][200]['content']['application/json'];
type Grant = GrantList['items'][number];

export function WorkspaceConnectionGrants({ workspaceId, csrfToken }: { workspaceId: string; csrfToken: string }) {
  const path = `/api/v1/admin/workspaces/${encodeURIComponent(workspaceId)}/connection-grants`;
  const [selected, setSelected] = useState<Grant>();
  const grants = useQuery<GrantList>({
    queryKey: ['workspaces', 'connection-grants', workspaceId],
    queryFn: async ({ signal }) => {
      const response = await fetch(path, { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
  const revoke = useMutation({
    mutationFn: async (grant: Grant) => {
      const response = await fetch(`${path}/${encodeURIComponent(grant.id)}`, {
        method: 'DELETE', headers: { 'X-CSRF-Token': csrfToken },
      });
      if (!response.ok) throw await readProblem(response);
    },
    onSuccess: async () => {
      await grants.refetch();
      setSelected(undefined);
    },
  });
  return (
    <Stack component="section" aria-label="Administrator connection grants" gap="sm">
      <Title order={2} size="h3">Administrator connection grants</Title>
      {grants.isPending ? <Text role="status">Loading connection grants…</Text> : null}
      {grants.isError ? <APIProblemView error={grants.error} /> : null}
      {grants.data?.items.length === 0 ? <Text size="sm">No active administrator connection grants.</Text> : null}
      {grants.data?.items.length ? (
        <Table aria-label="Active connection grants">
          <Table.Thead><Table.Tr><Table.Th>Administrator</Table.Th><Table.Th>Action</Table.Th></Table.Tr></Table.Thead>
          <Table.Tbody>
            {grants.data.items.map(grant => (
              <Table.Tr key={grant.id}>
                <Table.Td>{grant.email}</Table.Td>
                <Table.Td><Button variant="subtle" color="red" aria-label={`Revoke access for ${grant.email}`}
                  onClick={() => { revoke.reset(); setSelected(grant); }}>Revoke access</Button></Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      ) : null}
      <Modal opened={!!selected} onClose={() => { if (!revoke.isPending) setSelected(undefined); }}
        withCloseButton={!revoke.isPending} title="Revoke connection grant">
        <Stack>
          <Text size="sm">Connections using this grant for {selected?.email} will close immediately.</Text>
          {revoke.isError ? <APIProblemView error={revoke.error} /> : null}
          <Group justify="end">
            <Button variant="default" data-autofocus disabled={revoke.isPending}
              onClick={() => setSelected(undefined)}>Cancel</Button>
            <Button color="red" loading={revoke.isPending} onClick={() => { if (selected) revoke.mutate(selected); }}>Revoke access</Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}
