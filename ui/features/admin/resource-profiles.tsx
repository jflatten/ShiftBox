import { useRef, useState } from 'react';
import { Button, Chip, Group, Menu, Modal, NumberInput, Paper, Select, Stack, Switch, Table, Text, TextInput, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { APIProblemError, readProblem } from '../../lib/api-problem';
import type { components, operations } from '../../lib/api-types';
import { validLimit, type InfrastructureProvider } from '../../lib/use-infrastructure';

const endpoint = '/api/v1/admin/resource-profiles';
const mibPerGiB = 1024;
const computeFields = [
  { key: 'cpuCount', label: 'vCPUs' },
  { key: 'memoryMiB', label: 'Memory (MiB)' },
] as const;
const fields = [...computeFields, { key: 'diskMiB', label: 'Disk (MiB)' }] as const;
const numberStyle = { textAlign: 'right', fontVariantNumeric: 'tabular-nums' } as const;
const format = new Intl.NumberFormat();

type Profile = components['schemas']['ResourceProfile'];
type TemplateCatalog = operations['listTemplateCatalog']['responses'][200]['content']['application/json'];
type Dialog = { action: 'create' } | { action: 'edit' | 'delete'; profile: Profile };
type FormValues = { name: string; enabled: boolean; platformArtifactId: string | null; instanceType: string | null } & Record<'cpuCount' | 'memoryMiB' | 'diskSize', string | number>;

export function ResourceProfiles({ userId, csrfToken, provider }: {
  userId: string;
  csrfToken?: string;
  provider?: InfrastructureProvider;
}) {
  const ec2 = provider?.name === 'ec2';
  const canEdit = !!provider && (!ec2 || !!provider.instanceTypes?.length);
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
          <Text size="sm" c="dimmed">Set the resources a project allocates to each workspace.</Text>
        </Stack>
        {csrfToken ? <Button ref={addButton} disabled={!canEdit} onClick={event => open({ action: 'create' }, event.currentTarget)}>New resource profile</Button> : null}
      </Group>
      {csrfToken && ec2 && !canEdit ? <Text size="sm" role="status">
        No compatible EC2 instance types are available in the workspace Availability Zone.
      </Text> : null}
      {profiles.isError ? <Stack gap="xs" align="flex-start">
        <APIProblemView error={profiles.error} />
        <Button variant="default" onClick={() => void profiles.refetch()}>Retry loading resource profiles</Button>
      </Stack> : null}
      <Table.ScrollContainer minWidth={ec2 ? 720 : 560} type="native" tabIndex={0} role="region" aria-label="Resource profiles table">
        <Table aria-labelledby="resource-profiles-heading" highlightOnHover horizontalSpacing="md"
          verticalSpacing="xs" styles={{ thead: { backgroundColor: 'var(--mantine-color-default-hover)' } }}>
          <Table.Thead>
            <Table.Tr h={48}>
              <Table.Th scope="col">Name</Table.Th>
              {ec2 ? <><Table.Th scope="col">AMI image</Table.Th><Table.Th scope="col">EC2 instance type</Table.Th></> : null}
              {fields.map(field => <Table.Th key={field.key} scope="col" style={numberStyle}>
                {ec2 && field.key === 'diskMiB' ? 'EBS disk (GiB)' : field.label}
              </Table.Th>)}
              <Table.Th scope="col">Status</Table.Th>
              {csrfToken ? <Table.Th scope="col" ta="right">Actions</Table.Th> : null}
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {profiles.data?.map(profile => <Table.Tr key={profile.id} h={48}>
              <Table.Th scope="row" style={{ overflowWrap: 'anywhere', maxWidth: 280 }}>{profile.name}</Table.Th>
              {ec2 ? <><Table.Td>{profile.platformImageName || 'Select an AMI'}</Table.Td><Table.Td>{profile.instanceType ?? '—'}</Table.Td></> : null}
              {fields.map(field => <Table.Td key={field.key} style={numberStyle}>
                {format.format(ec2 && field.key === 'diskMiB' ? profile.diskMiB / mibPerGiB : profile[field.key])}
              </Table.Td>)}
              <Table.Td>
                <Chip checked readOnly hidden icon={false} color={profile.enabled ? 'green' : 'gray'} variant="light"
                  styles={{ label: { pointerEvents: 'none' } }} size="xs">
                  {profile.enabled ? 'Enabled' : 'Disabled'}
                </Chip>
              </Table.Td>
              {csrfToken ? <Table.Td ta="right">
                <ProfileActions profile={profile} canEdit={canEdit} open={open} />
              </Table.Td> : null}
            </Table.Tr>)}
            {profiles.isPending || profiles.data?.length === 0 ? <Table.Tr>
              <Table.Td colSpan={5 + Number(!!csrfToken) + 2 * Number(ec2)} py="md">
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
    {dialog && csrfToken ? <ProfileDialog dialog={dialog} userId={userId} csrfToken={csrfToken} provider={provider} close={close}
      saved={() => saved(dialog.action)} reload={async () => {
        if (!(await profiles.refetch()).isError) close();
      }} /> : null}
  </Paper>;
}

function ProfileActions({ profile, canEdit, open }: { profile: Profile; canEdit: boolean; open: (next: Dialog, trigger: HTMLButtonElement | null) => void }) {
  const trigger = useRef<HTMLButtonElement>(null);
  return <Menu position="bottom-end" transitionProps={{ duration: 0 }}>
    <Menu.Target>
      <Button ref={trigger} size="compact-md" variant="subtle" aria-label={`Actions for ${profile.name}`}>···</Button>
    </Menu.Target>
    <Menu.Dropdown>
      {canEdit ? <>
        <Menu.Item onClick={() => open({ action: 'edit', profile }, trigger.current)}>Edit</Menu.Item>
        <Menu.Divider />
      </> : null}
      <Menu.Item color="red" onClick={() => open({ action: 'delete', profile }, trigger.current)}>Delete</Menu.Item>
    </Menu.Dropdown>
  </Menu>;
}

function ProfileDialog({ dialog, userId, csrfToken, provider, close, saved, reload }: {
  dialog: Dialog;
  userId: string;
  csrfToken: string;
  provider?: InfrastructureProvider;
  close: () => void;
  saved: () => Promise<void>;
  reload: () => Promise<void>;
}) {
  const profile = dialog.action === 'create' ? undefined : dialog.profile;
  const deleting = dialog.action === 'delete';
  const ec2 = provider?.name === 'ec2';
  const instanceTypes = provider?.instanceTypes ?? [];
  const catalog = useQuery<TemplateCatalog>({
    queryKey: ['template-catalog', userId],
    enabled: ec2 && !deleting,
    queryFn: async ({ signal }) => {
      const response = await fetch('/api/v1/templates/catalog', { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
  });
  const images = catalog.isError ? [] : (catalog.data?.platformImages ?? []).filter(image => image.providerKind === 'ec2');
  const minimumDisk = provider?.minimumDiskGiB ?? 0;
  const maximumDisk = provider?.maximumDiskGiB ?? 0;
  const form = useForm<FormValues>({
    mode: 'uncontrolled',
    initialValues: {
      name: profile?.name ?? '',
      cpuCount: profile?.cpuCount ?? '',
      memoryMiB: profile?.memoryMiB ?? '',
      diskSize: ec2 ? (profile ? profile.diskMiB / mibPerGiB : minimumDisk) : profile?.diskMiB ?? '',
      enabled: profile?.enabled ?? true,
      instanceType: profile?.instanceType ?? null,
      platformArtifactId: profile?.platformArtifactId ?? null,
    },
    validate: {
      name: value => value.trim() ? null : 'Enter a profile name.',
      cpuCount: value => ec2 ? null : validLimit(value),
      memoryMiB: value => ec2 ? null : validLimit(value),
      platformArtifactId: value => !ec2 || images.some(image => image.id === value)
        ? null : 'Select an approved AMI image.',
      instanceType: (value, values) => !ec2 || instanceTypes.some(instance => instance.name === value &&
        instance.architecture === images.find(image => image.id === values.platformArtifactId)?.architecture)
        ? null : 'Select an offered EC2 instance type.',
      diskSize: (value, values) => {
        if (!ec2) return validLimit(value);
        const minimum = Math.max(minimumDisk, images.find(image => image.id === values.platformArtifactId)?.minimumDiskGiB ?? 0);
        return typeof value === 'number' && Number.isSafeInteger(value) && value >= minimum && value <= maximumDisk
          ? null : `Enter a whole number from ${format.format(minimum)} to ${format.format(maximumDisk)} GiB.`;
      },
    },
  });
  const imageSelection = form.useWatchValue('platformArtifactId');
  const selectedImage = images.find(image => image.id === imageSelection);
  const imageMinimumDisk = Math.max(minimumDisk, selectedImage?.minimumDiskGiB ?? 0);
  const compatibleTypes = instanceTypes.filter(instance => selectedImage && instance.architecture === selectedImage.architecture);
  const selection = form.useWatchValue('instanceType');
  const selected = compatibleTypes.find(instance => instance.name === selection);
  const mutation = useMutation({
    networkMode: 'always',
    mutationFn: async (values?: FormValues) => {
      const headers: Record<string, string> = { 'X-CSRF-Token': csrfToken };
      if (!navigator.onLine) throw new Error('Network connection unavailable');
      if (profile) headers['If-Match'] = `"${profile.version}"`;
      if (values) headers['Content-Type'] = 'application/json';
      const body = values ? {
        name: values.name.trim(),
        ...(ec2 ? { platformArtifactId: values.platformArtifactId, instanceType: values.instanceType, diskMiB: Number(values.diskSize) * mibPerGiB }
          : { cpuCount: values.cpuCount, memoryMiB: values.memoryMiB, diskMiB: values.diskSize }),
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
    }}>Reload profiles</Button> : <Button type="submit" color={deleting ? 'red' : undefined} loading={busy}
      disabled={ec2 && !deleting && (!catalog.isSuccess || !images.length)}>{submitLabel}</Button>}
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
        {ec2 ? <>
          {catalog.isPending ? <Text size="sm" role="status">Loading AMI images…</Text> : null}
          {catalog.isError ? <>
            <APIProblemView error={catalog.error} />
            <Button variant="default" onClick={() => void catalog.refetch()}>Retry loading AMI images</Button>
          </> : null}
          {catalog.isSuccess && !images.length ? <Text size="sm" role="status">
            No approved AMI images are available. Import and validate a platform image first.
          </Text> : null}
          <Select label="AMI image" required searchable allowDeselect={false}
            placeholder="Select an approved AMI image" nothingFoundMessage="No matching AMI images"
            description={selectedImage ? `${selectedImage.architecture === 'arm64' ? 'ARM64' : 'x86-64 (amd64)'}${selectedImage.amiId ? ` · ${selectedImage.amiId}` : ''}` : 'The resource profile supplies this image to projects.'}
            data={images.map(image => ({ value: image.id, label: image.name }))}
            disabled={busy || !catalog.isSuccess || !images.length}
            key={form.key('platformArtifactId')} {...form.getInputProps('platformArtifactId')}
            onChange={value => {
              form.setFieldValue('platformArtifactId', value);
              const architecture = images.find(image => image.id === value)?.architecture;
              if (!instanceTypes.some(instance => instance.name === form.getValues().instanceType && instance.architecture === architecture)) {
                form.setFieldValue('instanceType', null);
              }
            }} />
          <Select label="EC2 instance type" required searchable limit={40} allowDeselect={false}
            placeholder={selectedImage ? 'Search instance types' : 'Select an AMI image first'}
            data={compatibleTypes.map(instance => instance.name)}
            nothingFoundMessage="No matching EC2 instance types"
            disabled={busy || !selectedImage} key={form.key('instanceType')} {...form.getInputProps('instanceType')} />
          {selectedImage && !compatibleTypes.length ? <Text size="sm" role="status">No compatible EC2 instance types are available for this AMI in the workspace Availability Zone.</Text> : null}
          <Group grow>
            <TextInput label="vCPUs" readOnly value={selected ? format.format(selected.cpuCount) : ''} />
            <TextInput label="Memory (MiB)" readOnly value={selected ? format.format(selected.memoryMiB) : ''} />
          </Group>
        </> : computeFields.map(field => <NumberInput key={form.key(field.key)} label={field.label} required allowNegative={false}
          allowDecimal={false} disabled={busy} {...form.getInputProps(field.key)} />)}
        <NumberInput label={ec2 ? 'EBS disk (GiB)' : 'Disk (MiB)'} required allowNegative={false} allowDecimal={false}
          description={ec2 ? `Choose ${format.format(imageMinimumDisk)}–${format.format(maximumDisk)} GiB.` : undefined}
          disabled={busy} key={form.key('diskSize')} {...form.getInputProps('diskSize')} />
        {profile ? <Switch label="Enabled" aria-label="Enabled" description="Disabling prevents future workspace launches that use this profile." disabled={busy}
          key={form.key('enabled')} {...form.getInputProps('enabled', { type: 'checkbox' })} /> : null}
        {error}
        {buttons}
      </Stack>
    </form>}
  </Modal>;
}
