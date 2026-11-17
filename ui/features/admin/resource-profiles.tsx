import { useRef, useState } from 'react';
import { Button, Chip, Group, Menu, Modal, NumberInput, Paper, Stack, Switch, Table, Text, TextInput, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { APIProblemError, readProblem } from '../../lib/api-problem';
import type { components } from '../../lib/api-types';
import { validLimit, type Capacity } from '../../lib/use-infrastructure';

const endpoint = '/api/v1/admin/resource-profiles';
const fields = [
  { key: 'cpuCount', label: 'vCPUs' },
  { key: 'memoryMiB', label: 'Memory (MiB)' },
  { key: 'diskMiB', label: 'Disk (MiB)' },
] as const;
const numberStyle = { textAlign: 'right', fontVariantNumeric: 'tabular-nums' } as const;
const format = new Intl.NumberFormat();

type Profile = components['schemas']['ResourceProfile'];
type Dialog = { action: 'create' } | { action: 'edit' | 'delete'; profile: Profile };
type FormValues = { name: string; enabled: boolean } & Record<keyof Capacity, string | number>;

export function ResourceProfiles({ userId, csrfToken }: { userId: string; csrfToken?: string }) {
  const queryClient = useQueryClient();
  const queryKey = ['resource-profiles', userId];
  const profiles = useQuery<Profile[]>({
    queryKey,
    queryFn: async ({ signal }) => {
      const response = await fetch(endpoint, { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
  const [dialog, setDialog] = useState<Dialog>();
  const [message, setMessage] = useState('');
  const addButton = useRef<HTMLButtonElement>(null);
  const returnFocus = useRef<HTMLButtonElement>(null);

  function open(next: Dialog, trigger: HTMLButtonElement | null) {
    returnFocus.current = trigger;
    setMessage('');
    setDialog(next);
  }

  function close() {
    setDialog(undefined);
    requestAnimationFrame(() => {
      const trigger = returnFocus.current;
      (trigger?.isConnected ? trigger : addButton.current)?.focus();
    });
  }

  async function saved(action: Dialog['action']) {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey }),
      queryClient.invalidateQueries({ queryKey: ['template-catalog'] }),
    ]);
    setMessage({ create: 'Resource profile created.', edit: 'Resource profile updated.', delete: 'Resource profile deleted.' }[action]);
    close();
  }

  return <Paper component="section" aria-labelledby="resource-profiles-heading" withBorder radius="md" p="lg" miw={0}>
    <Stack gap="md">
      <Group justify="space-between" align="flex-end">
        <Stack gap={4}>
          <Title order={2} size="h3" id="resource-profiles-heading">Resource profiles</Title>
          <Text size="sm" c="dimmed">Set the resources a template allocates to each workspace.</Text>
        </Stack>
        {csrfToken ? <Button ref={addButton} onClick={event => open({ action: 'create' }, event.currentTarget)}>New resource profile</Button> : null}
      </Group>
      {profiles.isError ? <Stack gap="xs" align="flex-start">
        <APIProblemView error={profiles.error} />
        <Button variant="default" onClick={() => void profiles.refetch()}>Retry loading resource profiles</Button>
      </Stack> : null}
      <Table.ScrollContainer minWidth={560} type="native" tabIndex={0} role="region" aria-label="Resource profiles table">
        <Table aria-labelledby="resource-profiles-heading" highlightOnHover horizontalSpacing="md"
          verticalSpacing="xs" styles={{ thead: { backgroundColor: 'var(--mantine-color-default-hover)' } }}>
          <Table.Thead>
            <Table.Tr h={48}>
              <Table.Th scope="col">Name</Table.Th>
              {fields.map(field => <Table.Th key={field.key} scope="col" style={numberStyle}>{field.label}</Table.Th>)}
              <Table.Th scope="col">Status</Table.Th>
              {csrfToken ? <Table.Th scope="col" ta="right">Actions</Table.Th> : null}
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {profiles.data?.map(profile => <Table.Tr key={profile.id} h={48}>
              <Table.Th scope="row" style={{ overflowWrap: 'anywhere', maxWidth: 280 }}>{profile.name}</Table.Th>
              {fields.map(field => <Table.Td key={field.key} style={numberStyle}>{format.format(profile[field.key])}</Table.Td>)}
              <Table.Td>
                <Chip checked readOnly hidden icon={false} color={profile.enabled ? 'green' : 'gray'} variant="light"
                  styles={{ label: { pointerEvents: 'none' } }} size="xs">
                  {profile.enabled ? 'Enabled' : 'Disabled'}
                </Chip>
              </Table.Td>
              {csrfToken ? <Table.Td ta="right">
                <ProfileActions profile={profile} open={open} />
              </Table.Td> : null}
            </Table.Tr>)}
            {profiles.isPending || profiles.data?.length === 0 ? <Table.Tr>
              <Table.Td colSpan={csrfToken ? 6 : 5} py="md">
                <Text c="dimmed" size="sm" role="status">
                  {profiles.isPending ? 'Loading resource profiles…' : 'No resource profiles yet.'}
                </Text>
              </Table.Td>
            </Table.Tr> : null}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      {message ? <Text size="sm" role="status">{message}</Text> : null}
    </Stack>
    {dialog && csrfToken ? <ProfileDialog dialog={dialog} csrfToken={csrfToken} close={close}
      saved={() => saved(dialog.action)} reload={async () => {
        if (!(await profiles.refetch()).isError) close();
      }} /> : null}
  </Paper>;
}

function ProfileActions({ profile, open }: { profile: Profile; open: (next: Dialog, trigger: HTMLButtonElement | null) => void }) {
  const trigger = useRef<HTMLButtonElement>(null);
  return <Menu position="bottom-end" transitionProps={{ duration: 0 }}>
    <Menu.Target>
      <Button ref={trigger} size="compact-md" variant="subtle" aria-label={`Actions for ${profile.name}`}>···</Button>
    </Menu.Target>
    <Menu.Dropdown>
      <Menu.Item onClick={() => open({ action: 'edit', profile }, trigger.current)}>Edit</Menu.Item>
      <Menu.Divider />
      <Menu.Item color="red" onClick={() => open({ action: 'delete', profile }, trigger.current)}>Delete</Menu.Item>
    </Menu.Dropdown>
  </Menu>;
}

function ProfileDialog({ dialog, csrfToken, close, saved, reload }: {
  dialog: Dialog;
  csrfToken: string;
  close: () => void;
  saved: () => Promise<void>;
  reload: () => Promise<void>;
}) {
  const profile = dialog.action === 'create' ? undefined : dialog.profile;
  const deleting = dialog.action === 'delete';
  const form = useForm<FormValues>({
    mode: 'uncontrolled',
    initialValues: profile ?? { name: '', cpuCount: '', memoryMiB: '', diskMiB: '', enabled: true },
    validate: {
      name: value => value.trim() ? null : 'Enter a profile name.',
      cpuCount: validLimit,
      memoryMiB: validLimit,
      diskMiB: validLimit,
    },
  });
  const mutation = useMutation({
    networkMode: 'always',
    mutationFn: async (values?: FormValues) => {
      const headers: Record<string, string> = { 'X-CSRF-Token': csrfToken };
      if (!navigator.onLine) throw new Error('Network connection unavailable');
      if (profile) headers['If-Match'] = `"${profile.version}"`;
      if (values) headers['Content-Type'] = 'application/json';
      const body = values ? {
        name: values.name.trim(), cpuCount: values.cpuCount, memoryMiB: values.memoryMiB, diskMiB: values.diskMiB,
        ...(profile ? { enabled: values.enabled } : {}),
      } : undefined;
      const url = profile ? `${endpoint}/${encodeURIComponent(profile.id)}` : endpoint;
      const method = deleting ? 'DELETE' : profile ? 'PUT' : 'POST';
      const response = await fetch(url, { method, headers, body: body && JSON.stringify(body) });
      if (!response.ok) throw await readProblem(response);
    },
    onSuccess: saved,
  });
  const [reloading, setReloading] = useState(false);
  const busy = mutation.isPending || reloading;
  const refreshRequired = mutation.error instanceof APIProblemError && [404, 412].includes(mutation.error.problem.status);
  const title = { create: 'New resource profile', edit: 'Edit resource profile', delete: 'Delete resource profile' }[dialog.action];
  const submitLabel = { create: 'Create resource profile', edit: 'Save changes', delete: 'Delete resource profile' }[dialog.action];

  function submit(values?: FormValues) {
    if (!busy && !refreshRequired) mutation.mutate(values);
  }

  const error = mutation.error ? <APIProblemView error={mutation.error} /> : null;
  const buttons = <Group justify="flex-end">
    <Button variant="default" disabled={busy} onClick={close} data-autofocus={deleting || undefined}>Cancel</Button>
    {refreshRequired ? <Button loading={reloading} onClick={async () => {
      setReloading(true);
      try {
        await reload();
      } finally {
        setReloading(false);
      }
    }}>Reload profiles</Button> : <Button type="submit" color={deleting ? 'red' : undefined} loading={busy}>{submitLabel}</Button>}
  </Group>;

  return <Modal opened title={title} onClose={close} returnFocus={false}
    closeOnEscape={!busy} closeOnClickOutside={!busy} withCloseButton={!busy} closeButtonProps={{ 'aria-label': 'Close dialog' }} transitionProps={{ duration: 0 }}>
    {deleting ? <form onSubmit={event => { event.preventDefault(); submit(); }}>
      <Stack>
        <Text>Delete <strong>{profile?.name}</strong> permanently?</Text>
        {error}
        {buttons}
      </Stack>
    </form> : <form noValidate onSubmit={form.onSubmit(submit, errors => {
      const first = Object.keys(errors)[0];
      if (first) form.getInputNode(first)?.focus();
    })}>
      <Stack>
        <TextInput label="Profile name" data-autofocus maxLength={120} required disabled={busy}
          key={form.key('name')} {...form.getInputProps('name')} />
        {fields.map(field => <NumberInput key={form.key(field.key)} label={field.label} required allowNegative={false}
          allowDecimal={false} disabled={busy} {...form.getInputProps(field.key)} />)}
        {profile ? <Switch label="Enabled" aria-label="Enabled" description="Disabling prevents future workspace launches that use this profile." disabled={busy}
          key={form.key('enabled')} {...form.getInputProps('enabled', { type: 'checkbox' })} /> : null}
        {error}
        {buttons}
      </Stack>
    </form>}
  </Modal>;
}
