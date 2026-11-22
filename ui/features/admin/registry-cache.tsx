'use client';

import { useEffect, useId, useState } from 'react';
import { Alert, Button, Chip, Group, NumberInput, Pagination, Paper, Progress, SimpleGrid, Stack, Switch, Table, Text, TextInput, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { APIProblemView } from '../../components/api-problem';
import { WorkflowStepper } from '../../components/workflow-stepper';
import { APIProblemError } from '../../lib/api-problem';
import { saveImageCache, useImageCache, useSaveBuiltImagePolicy,
  type BuiltImagePolicy, type ImageCacheSettings, type ImageCacheSnapshot } from '../../lib/use-image-cache';
import { useOperation } from '../../lib/use-operation';

const mib = 1048576n;
const maxBytes = 9223372036854775807n;
const labels = {
  ready: 'Ready', disabled: 'Disabled', degraded: 'Degraded', available: 'Available', unavailable: 'Unavailable',
  pressure: 'Under pressure', unknown: 'Unknown', staged: 'Pending', draining: 'Finishing active pulls', failed: 'Failed',
  on: 'Saved On', off: 'Saved Off',
};
const colors = { ready: 'green', disabled: 'gray', degraded: 'yellow', available: 'green', unavailable: 'red',
  pressure: 'yellow', unknown: 'gray', staged: 'blue', draining: 'blue', failed: 'red', on: 'green', off: 'gray' };

export function RegistryCache({ userId, active, csrfToken }: { userId: string; active: boolean; csrfToken?: string }) {
  const query = useImageCache(userId, active);
  const [operationId, setOperationId] = useState<string>();
  const data = query.isError ? undefined : query.data;
  const refresh = query.refetch;
  const pending = data?.activation.phase === 'staged' || data?.activation.phase === 'draining';
  const currentOperation = pending ? data.activation.operationId ?? operationId : operationId;

  return <Stack gap="lg">
    {query.isPending && active ? <Text role="status" aria-label="Loading registry cache">Loading registry cache…</Text> : null}
    {query.isError ? <Stack align="flex-start">
      <APIProblemView error={query.error} />
      <Button variant="default" onClick={() => void refresh()}>Retry</Button>
    </Stack> : null}
    {data ? <>
      <BuiltImageSettings policy={data.builtImages} userId={userId} csrfToken={csrfToken} refresh={refresh} />
      <Paper component="section" aria-labelledby="registry-cache-settings" withBorder radius="md" p="lg">
        <Stack gap="md">
          <Group justify="space-between">
            <Title order={2} size="h3" id="registry-cache-settings">Registry cache settings</Title>
            <Button variant="default" loading={query.isFetching} onClick={() => void refresh()}>Refresh</Button>
          </Group>
          <Group>
            <StateChip state={data.health.state} />
            {pending ? <StateChip state={data.activation.phase} /> : null}
          </Group>
          <Text size="sm" style={{ overflowWrap: 'anywhere' }}>
            Effective version {data.effective.version} · {data.effective.enabled ? 'Retention enabled' : 'Retention disabled'} · {data.effective.storagePath}
          </Text>
          <Text size="sm">Effective budget {formatBytes(data.effective.maxBytes)} · Watermarks {data.effective.volumeLowPercent}% low and {data.effective.volumeHighPercent}% high</Text>
          {data.version !== data.effective.version ? <Text size="sm">Desired version {data.version}</Text> : null}
          {!csrfToken && data.version !== data.effective.version ? <Text size="sm" style={{ overflowWrap: 'anywhere' }}>
            Desired retention {data.desired.enabled ? 'enabled' : 'disabled'} · {data.desired.storagePath} · Budget {formatBytes(data.desired.maxBytes)} ·
            Watermarks {data.desired.volumeLowPercent}% low and {data.desired.volumeHighPercent}% high
          </Text> : null}
          {data.health.guestEndpoint ? <Text size="sm" style={{ overflowWrap: 'anywhere' }}>Guest endpoint {data.health.guestEndpoint}</Text> : null}
          {data.health.detail ? <Alert color="yellow">{data.health.detail}</Alert> : null}
          {data.activation.error ? <Alert color="red" title="Cache activation failed">{data.activation.error}</Alert> : null}
          {csrfToken ? <CacheSettingsForm snapshot={data} csrfToken={csrfToken} pending={pending}
            onSave={async id => { setOperationId(id); await refresh(); }} /> : null}
          {active && csrfToken && currentOperation && (pending || !data.activation.error) ? <CacheActivation id={currentOperation}
            refresh={refresh} complete={() => setOperationId(undefined)} /> : null}
        </Stack>
      </Paper>
      <CacheUsage snapshot={data} />
      <CachedImages images={data.cachedImages} />
    </> : null}
  </Stack>;
}

function StateChip({ state }: { state: keyof typeof labels }) {
  return <Chip checked readOnly hidden icon={false} color={colors[state]} variant="light"
    styles={{ label: { cursor: 'default' } }} wrapperProps={{ role: 'status' }}>{labels[state]}</Chip>;
}

function BuiltImageSettings({ policy, userId, csrfToken, refresh }: {
  policy: BuiltImagePolicy;
  userId: string;
  csrfToken?: string;
  refresh: () => Promise<unknown>;
}) {
  const [draft, setDraft] = useState<{ enabled: boolean; version: number }>();
  const save = useSaveBuiltImagePolicy(userId);
  const staleResponse = save.error instanceof APIProblemError && save.error.problem.status === 412;
  const stale = staleResponse || (!save.isPending && draft !== undefined && draft.version !== policy.version);
  const label = 'Built devcontainer images';
  const description = 'Reuse finished devcontainer images on later launches in the same project.';

  return <Paper component="section" aria-label={label} withBorder radius="md" p="lg">
    <form onSubmit={event => {
      event.preventDefault();
      if (!csrfToken || !draft || stale || save.isPending) return;
      save.mutate({ ...draft, csrfToken }, { onSuccess: () => setDraft(undefined) });
    }}>
      <Stack gap="md">
        <Group align="flex-start" justify="space-between">
          {csrfToken ? <Switch label={label} aria-label={label} description={description} checked={draft?.enabled ?? policy.enabled}
            disabled={save.isPending} onChange={event => {
              const enabled = event.currentTarget.checked;
              setDraft(enabled === policy.enabled ? undefined : { enabled, version: draft?.version ?? policy.version });
              if (!staleResponse) save.reset();
            }} />
            : <Stack gap={4}><Text fw={600}>{label}</Text><Text c="dimmed" size="sm">{description}</Text></Stack>}
          <Group gap="xs">
            <StateChip state={policy.enabled ? 'on' : 'off'} />
            {policy.enabled ? <StateChip state={policy.availability === 'available' ? 'available' : 'unavailable'} /> : null}
          </Group>
        </Group>
        {policy.enabled && policy.availability === 'unavailable'
          ? <Text size="sm">Check the registry cache storage path and budget to restore built image reuse.</Text> : null}
        {csrfToken && policy.enabled && draft?.enabled === false ? <Alert color="yellow">
          Turning this off stops built image reuse across the organization and removes stored built images in the background.
        </Alert> : null}
        {csrfToken && save.isError ? <APIProblemView error={save.error} /> : null}
        {csrfToken && stale ? <Group gap="xs">
          {!staleResponse ? <Text size="sm">Built image settings changed while you were editing.</Text> : null}
          <Button variant="subtle" onClick={async () => {
            await refresh();
            setDraft(undefined);
            save.reset();
          }}>Load current built image settings</Button>
        </Group> : null}
        {csrfToken ? <Button type="submit" disabled={!draft || stale || save.isPending}
          loading={save.isPending} style={{ alignSelf: 'flex-start' }}>Save built image settings</Button> : null}
      </Stack>
    </form>
  </Paper>;
}

function sizeDraft(bytes: string | null) {
  if (bytes === null) return '';
  return BigInt(bytes) % mib === 0n ? String(BigInt(bytes) / mib) : '';
}

function CacheSettingsForm({ snapshot, csrfToken, pending, onSave }: {
  snapshot: ImageCacheSnapshot;
  csrfToken: string;
  pending: boolean;
  onSave: (operation?: string) => Promise<void>;
}) {
  const [base, setBase] = useState(snapshot);
  const [error, setError] = useState<unknown>();
  const [saving, setSaving] = useState(false);
  const form = useForm({
    mode: 'controlled',
    initialValues: { ...base.desired, sizeMiB: sizeDraft(base.desired.maxBytes) as string | number },
    validate: {
      storagePath: value => value.startsWith('/') && value !== '/' && !value.endsWith('/') && !value.split('/').slice(1).some(part => ['', '.', '..'].includes(part))
        ? null : 'Enter a canonical absolute directory.',
      sizeMiB: (value, values) => String(value) === '' && base.desired.maxBytes !== null && sizeDraft(base.desired.maxBytes) === '' ? null
        : String(value) === '' && !values.enabled ? null
        : /^[1-9][0-9]*$/.test(String(value)) && BigInt(value) * mib <= maxBytes ? null : 'Enter a positive whole MiB size.',
      volumeHighPercent: (value, values) => Number.isInteger(value) && value < 100 && value > values.volumeLowPercent
        ? null : 'Enter a whole percentage above the low watermark and below 100.',
      volumeLowPercent: (value, values) => Number.isInteger(value) && value > 0 && value < values.volumeHighPercent
        ? null : 'Enter a whole percentage above zero and below the high watermark.',
    },
  });
  const { isDirty, setInitialValues, setValues, resetDirty, getInputNode } = form;
  const dirty = isDirty();
  useEffect(() => {
    if (snapshot.version === base.version || dirty) return;
    const values = { ...snapshot.desired, sizeMiB: sizeDraft(snapshot.desired.maxBytes) };
    setBase(snapshot);
    setInitialValues(values);
    setValues(values);
    resetDirty(values);
  }, [snapshot, base.version, dirty, setInitialValues, setValues, resetDirty]);

  const disabled = saving || pending;
  return <form onSubmit={form.onSubmit(async values => {
    setSaving(true);
    setError(undefined);
    try {
      const desired: ImageCacheSettings = {
        enabled: values.enabled, storagePath: values.storagePath,
        maxBytes: String(values.sizeMiB) !== '' ? String(BigInt(values.sizeMiB) * mib)
          : sizeDraft(base.desired.maxBytes) === '' ? base.desired.maxBytes : null,
        volumeHighPercent: values.volumeHighPercent, volumeLowPercent: values.volumeLowPercent,
      };
      const id = await saveImageCache(desired, base.version, csrfToken);
      resetDirty(values);
      await onSave(id);
    } catch (error) {
      setError(error);
    } finally {
      setSaving(false);
    }
  }, errors => getInputNode(Object.keys(errors)[0])?.focus())}>
    <Stack gap="md">
      <Switch label="Retain cached image content" aria-label="Retain cached image content" description="Restricted workspaces can still stream public images through the controller when retention is disabled."
        disabled={disabled} {...form.getInputProps('enabled', { type: 'checkbox' })} />
      <TextInput label="Cache storage location" disabled={disabled}
        description="Moving the cache finishes active pulls and starts with an empty location. Files at the previous path stay on disk."
        {...form.getInputProps('storagePath')} />
      <SimpleGrid cols={{ base: 1, sm: 3 }}>
        <NumberInput label="Cache size (MiB)" disabled={disabled} allowDecimal={false} allowNegative={false} hideControls
          description={base.desired.maxBytes && sizeDraft(base.desired.maxBytes) === '' ? `Saved budget ${formatBytes(base.desired.maxBytes)}` : undefined}
          {...form.getInputProps('sizeMiB')} />
        <NumberInput label="High watermark (%)" disabled={disabled} allowDecimal={false} allowNegative={false} hideControls
          {...form.getInputProps('volumeHighPercent')} />
        <NumberInput label="Low watermark (%)" disabled={disabled} allowDecimal={false} allowNegative={false} hideControls
          {...form.getInputProps('volumeLowPercent')} />
      </SimpleGrid>
      {error !== undefined ? <APIProblemView error={error} /> : null}
      {base.version !== snapshot.version && dirty ? <Alert color="yellow">
        Settings changed while you were editing. Review your draft against the current settings before saving.
        <Button variant="subtle" onClick={() => {
          const values = { ...snapshot.desired, sizeMiB: sizeDraft(snapshot.desired.maxBytes) };
          setBase(snapshot);
          setInitialValues(values);
          setValues(values);
          resetDirty(values);
          setError(undefined);
        }}>Load current settings</Button>
      </Alert> : null}
      <Button type="submit" disabled={disabled || base.version !== snapshot.version || (!dirty && snapshot.activation.phase !== 'failed')}
        loading={saving} style={{ alignSelf: 'flex-start' }}>Save cache settings</Button>
    </Stack>
  </form>;
}

function CacheActivation({ id, refresh, complete }: { id: string; refresh: () => Promise<unknown>; complete: () => void }) {
  const operation = useOperation(id);
  const status = operation.data?.status;
  useEffect(() => {
    if (status !== 'succeeded' && status !== 'failed' && status !== 'cancelled') return;
    void refresh().then(complete);
  }, [status, refresh, complete]);
  return operation.data ? <WorkflowStepper operation={operation.data} failureTitle="Cache settings activation failed" />
    : operation.isError ? <APIProblemView error={operation.error} /> : null;
}

function formatBytes(value?: string | null) {
  return value == null ? 'Not measured' : `${BigInt(value).toLocaleString()} bytes`;
}

function percentage(part?: string | null, total?: string | null) {
  if (part == null || total == null || BigInt(total) <= 0n) return null;
  return Math.min(100, Number(BigInt(part) * 10000n / BigInt(total)) / 100);
}

function CacheUsage({ snapshot }: { snapshot: ImageCacheSnapshot }) {
  const usage = snapshot.usage;
  const stats = snapshot.effectiveness;
  const cachePercent = percentage(usage?.retainedBytes, usage?.budgetBytes);
  const volumePercent = percentage(usage?.volumeUsedBytes, usage?.volumeTotalBytes);
  return <Paper component="section" aria-labelledby="registry-cache-usage" withBorder radius="md" p="lg">
    <Stack gap="md">
      <Title order={2} size="h3" id="registry-cache-usage">Cache usage</Title>
      {cachePercent !== null ? <Stack gap={4}><Text size="sm">Retained content budget</Text><Progress value={cachePercent}
        aria-label="Retained content budget used" /></Stack> : null}
      {volumePercent !== null ? <Stack gap={4}><Text size="sm">Storage volume used</Text><Progress value={volumePercent}
        aria-label="Storage volume used" /></Stack> : null}
      <Measurements values={[
        ['Retained, including old locations', formatBytes(usage?.retainedBytes)], ['Hard budget', formatBytes(usage?.budgetBytes)],
        ['Temporary files', formatBytes(usage?.temporaryBytes)], ['Unwritten reservations', formatBytes(usage?.reservedBytes)],
        ['Retired storage', formatBytes(usage?.retiredBytes)], ['Volume capacity', formatBytes(usage?.volumeTotalBytes)],
        ['Volume used', formatBytes(usage?.volumeUsedBytes)], ['Volume available', formatBytes(usage?.volumeAvailableBytes)],
        ['Active transfers', usage?.activeTransfers ?? 'Not measured'], ['Evicted', formatBytes(usage?.lastEvictedBytes)],
        ['Last eviction', usage?.lastEvictionAt ? new Date(usage.lastEvictionAt).toLocaleString() : 'Not measured'],
        ['Measured at', usage ? new Date(usage.observedAt).toLocaleString() : 'Not measured'],
      ]} />
      <Title order={3} size="h4">Image payload reuse</Title>
      <Text size="sm">Controller payload reuse excludes registry metadata and applies only to traffic through the controller.</Text>
      {stats ? <Text size="sm">Measured since {new Date(stats.windowStartedAt).toLocaleString()}</Text> : null}
      <Measurements values={[
        ['Controller payload reuse', stats?.controllerReusePercent == null ? 'Not measured' : `${stats.controllerReusePercent.toFixed(1)}%`],
        ['Controller cached payload', formatBytes(stats?.cachedBytes)], ['Upstream image payload', formatBytes(stats?.upstreamBytes)],
        ['Upstream metadata', formatBytes(stats?.metadataBytes)], ['Streamed without retention (part of upstream)', formatBytes(stats?.streamedBytes)],
        ['Direct guest payload', formatBytes(stats?.directBytes)], ['Guest-local reuse', formatBytes(stats?.guestLocalBytes)],
      ]} />
      {snapshot.retiredLocations.length > 0 ? <Stack gap="xs">
        <Title order={3} size="h4">Retired cache locations</Title>
        <Text size="sm">Files stay at their previous locations. Operators can reclaim retired files after active transfers finish.</Text>
        <Measurements values={snapshot.retiredLocations.map(location => [location.storagePath, formatBytes(location.bytes)])} />
      </Stack> : null}
    </Stack>
  </Paper>;
}

function Measurements({ values }: { values: [string, string | number][] }) {
  return <SimpleGrid component="dl" cols={{ base: 1, sm: 2, lg: 4 }} m={0} spacing="md">
    {values.map(([label, value]) => <Stack key={label} gap={4} style={{ overflowWrap: 'anywhere' }}>
      <Text component="dt" size="xs" c="dimmed">{label}</Text>
      <Text component="dd" size="sm" fw={600} m={0}>{value}</Text>
    </Stack>)}
  </SimpleGrid>;
}

const imagesPerPage = 20;
const layerSizeFormat = new Intl.NumberFormat(undefined, { maximumFractionDigits: 2 });
const imageDateFormat = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });

function CachedImages({ images }: { images: ImageCacheSnapshot['cachedImages'] }) {
  const [page, setPage] = useState(1);
  const pageCount = Math.max(1, Math.ceil(images.length / imagesPerPage));
  const currentPage = Math.min(page, pageCount);
  const start = (currentPage - 1) * imagesPerPage;
  const pageImages = images.slice(start, start + imagesPerPage);

  useEffect(() => {
    if (page > pageCount) setPage(pageCount);
  }, [page, pageCount]);

  return <Paper component="section" aria-labelledby="registry-cache-images" withBorder radius="md" p="lg">
    <Stack gap="md">
      <Title order={2} size="h3" id="registry-cache-images">Cached images</Title>
      {images.length === 0 ? <Text size="sm" c="dimmed">No images are currently retained.</Text> : <>
        <Text size="sm" c="dimmed">Sizes show retained layer data for the listed platform. Images can share layers.</Text>
        <Table.ScrollContainer minWidth={1000}>
          <Table aria-labelledby="registry-cache-images" verticalSpacing="sm" highlightOnHover tabularNums>
            <Table.Thead bg="var(--mantine-color-default-hover)"><Table.Tr>
              <Table.Th scope="col">Image</Table.Th><Table.Th scope="col">Digest</Table.Th>
              <Table.Th scope="col">Platform</Table.Th><Table.Th scope="col">Availability</Table.Th>
              <Table.Th scope="col" ta="right">Total size (MB)</Table.Th>
              <Table.Th scope="col" ta="right">Layer sizes (MB)</Table.Th><Table.Th scope="col">Date added</Table.Th>
            </Table.Tr></Table.Thead>
            <Table.Tbody>{pageImages.map((image, index) => <Table.Tr
              key={`${image.source}/${image.reference}/${image.architecture}/${image.digest}/${start + index}`}>
              <Table.Th scope="row">
                <Text size="sm" fw={600} style={{ overflowWrap: 'anywhere' }}>{image.reference}</Text>
                <Text size="xs" c="dimmed">{image.source === 'devcontainer' ? 'Devcontainer build' : 'Public registry'}</Text>
              </Table.Th>
              <Table.Td><Text size="xs" ff="monospace" style={{ overflowWrap: 'anywhere' }}>{image.digest}</Text></Table.Td>
              <Table.Td>{image.architecture ? `linux/${image.architecture}` : 'Unknown'}</Table.Td>
              <Table.Td><Chip checked readOnly hidden icon={false} variant="light" color={image.availability === 'complete' ? 'green' : 'yellow'}
                styles={{ label: { cursor: 'default' } }}>{image.availability === 'complete' ? 'Complete' : 'Partial'}</Chip></Table.Td>
              <Table.Td ta="right"><Text size="sm" style={{ whiteSpace: 'nowrap' }}>{formatTotalSize(image)}</Text></Table.Td>
              <Table.Td ta="right"><LayerSizes sizes={image.layerBytes} partial={image.availability === 'partial'} /></Table.Td>
              <Table.Td style={{ whiteSpace: 'nowrap' }}>
                <Text component="time" size="sm" dateTime={image.addedAt}>{imageDateFormat.format(new Date(image.addedAt))}</Text>
              </Table.Td>
            </Table.Tr>)}</Table.Tbody>
          </Table>
        </Table.ScrollContainer>
        <Group justify="space-between" gap="sm">
          <Text size="sm" role="status">{start + 1}–{start + pageImages.length} of {images.length} images</Text>
          <Pagination total={pageCount} value={currentPage} onChange={setPage} hideWithOnePage size="sm"
            aria-label="Cached images pages" getItemProps={number => ({ 'aria-label': `Page ${number}` })} />
        </Group>
      </>}
    </Stack>
  </Paper>;
}

function LayerSizes({ sizes, partial }: { sizes: (string | null)[]; partial: boolean }) {
  const [expanded, setExpanded] = useState(false);
  const id = useId();
  if (sizes.length === 0) return <Text size="sm" c="dimmed">{partial ? 'Unavailable' : 'No layers'}</Text>;
  if (sizes.length === 1) return <Text size="sm">{formatLayerSize(sizes[0])}</Text>;

  return <Stack gap={4} align="end">
    <Button variant="subtle" size="compact-sm" aria-expanded={expanded} aria-controls={id}
      onClick={() => setExpanded(value => !value)}>{sizes.length} layers</Button>
    <Stack id={id} gap={4} hidden={!expanded}>
      {expanded ? sizes.map((size, index) => <Text key={index} size="xs" style={{ whiteSpace: 'nowrap' }}>
        Layer {index + 1} · {formatLayerSize(size)}
      </Text>) : null}
    </Stack>
  </Stack>;
}

function formatLayerSize(bytes: string | null) {
  return bytes === null ? 'Not cached' : formatSizeMB(BigInt(bytes));
}

function formatTotalSize({ layerBytes, availability }: ImageCacheSnapshot['cachedImages'][number]) {
  if (layerBytes.length === 0 && availability === 'partial') return 'Unavailable';
  const total = layerBytes.reduce((sum, bytes) => sum + BigInt(bytes ?? 0), 0n);
  return formatSizeMB(total);
}

function formatSizeMB(bytes: bigint) {
  const mb = Number(bytes) / 1000000;
  if (mb > 0 && mb < 0.01) return '<0.01 MB';
  return `${layerSizeFormat.format(mb)} MB`;
}
