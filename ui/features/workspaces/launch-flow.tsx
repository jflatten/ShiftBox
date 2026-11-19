import { useRef, useState } from 'react';
import { Anchor, Button, Group, Modal, SimpleGrid, Stack, Text, TextInput, Title } from '@mantine/core';
import { APIProblemView } from '../../components/api-problem';
import { WorkflowStepper } from '../../components/workflow-stepper';
import { readProblem } from '../../lib/api-problem';
import { providerLabel } from '../../lib/provider-label';
import { useOperation } from '../../lib/use-operation';
import type { TemplateRevision } from '../templates/use-templates';
import { LaunchProjectPicker } from './launch-project-picker';
import styles from './launch-project-picker.module.css';
import { useWorkspaceDetail } from './workspace-commands';
import { WorkspaceImageCache } from './workspace-image-cache';

interface AcceptedLaunch {
  id: string;
  workspaceId: string;
}

export function LaunchFlow({ csrfToken, userID, admin }: { csrfToken: string; userID: string; admin: boolean }) {
  const [name, setName] = useState('');
  const [review, setReview] = useState<TemplateRevision>();
  const [accepted, setAccepted] = useState<AcceptedLaunch>();
  const [error, setError] = useState<unknown>();
  const [submitting, setSubmitting] = useState(false);
  const requestKeys = useRef(new Map<string, string>());

  function closeReview() {
    if (submitting) return;
    setReview(undefined);
    setError(undefined);
  }

  const begin = async () => {
    if (!review || !name.trim() || submitting) return;
    setSubmitting(true);
    setError(undefined);
    try {
      const revision = JSON.stringify([templateKey(review), name.trim()]);
      const key = requestKeys.current.get(revision) ?? crypto.randomUUID();
      requestKeys.current.set(revision, key);
      const response = await fetch('/api/v1/workspaces', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'Idempotency-Key': key },
        body: JSON.stringify({ templateId: review.id, templateRevision: review.currentRevision, name: name.trim() }),
      });
      if (!response.ok) throw await readProblem(response);
      setAccepted(await response.json());
    } catch (error) {
      setError(error);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <>
      <Anchor href="/workspaces/">All workspaces</Anchor>
      <Title order={1}>Launch workspace</Title>
      {accepted ? <LaunchProgress accepted={accepted} /> : <LaunchProjectPicker userID={userID} admin={admin} onReview={setReview} />}
      <Modal opened={!!review && !accepted} onClose={closeReview} title="Review launch" centered size="lg" padding="lg"
        classNames={{ content: styles.surface }}
        styles={{ title: { fontWeight: 600, fontSize: 'var(--mantine-font-size-lg)' }, body: { paddingBottom: 0 } }}
        closeOnClickOutside={false} closeOnEscape={!submitting} closeButtonProps={{ 'aria-label': 'Close launch review', disabled: submitting }}>
        {review ? (
          <Stack component="form" gap="lg" onSubmit={event => { event.preventDefault(); void begin(); }}>
            <Title order={2} size="h4">{review.name} · Revision {review.currentRevision}</Title>
            <TextInput label="Workspace name" required maxLength={120} value={name} disabled={submitting} data-autofocus
              onChange={event => setName(event.currentTarget.value)} />
            <SimpleGrid component="dl" cols={{ base: 1, xs: 2 }} spacing="md" m={0}>
              <LaunchDetail label="Repository" value={review.repositoryUrl} />
              <LaunchDetail label="Repository ref" value={review.repositoryRef || 'Default branch'} />
              <LaunchDetail label="Dev Container configuration" value={review.devcontainerPath} />
              <LaunchDetail label="Resource profile" value={review.resourceProfileName || 'Unavailable'} />
              <LaunchDetail label="Provider" value={providerLabel(review.providerKind)} />
              <LaunchDetail label="Platform image" value={review.platformImageName || 'Unavailable'} />
              <LaunchDetail label="Secret references" value={review.secretNames?.join(', ') || 'None'} />
            </SimpleGrid>
            {error !== undefined ? <APIProblemView error={error} /> : null}
            <Group grow gap={0} mx="calc(-1 * var(--mantine-spacing-lg))" wrap="nowrap">
              <Button variant="default" radius={0} size="lg" disabled={submitting} onClick={closeReview}>Cancel</Button>
              <Button type="submit" radius={0} size="lg" disabled={!name.trim()} loading={submitting}>Launch workspace</Button>
            </Group>
          </Stack>
        ) : null}
      </Modal>
    </>
  );
}

function LaunchDetail({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <Text component="dt" size="sm" c="dimmed">{label}</Text>
      <Text component="dd" ml={0} style={{ overflowWrap: 'anywhere' }}>{value}</Text>
    </div>
  );
}

function templateKey(template: TemplateRevision) {
  return `${template.id}/${template.currentRevision}`;
}

function LaunchProgress({ accepted }: { accepted: AcceptedLaunch }) {
  const operation = useOperation(accepted.id);
  const workspace = useWorkspaceDetail(accepted.workspaceId, !operation.accessDenied);
  if (operation.accessDenied) return <Text role="alert">You no longer have access to this operation.</Text>;
  return (
    <Stack align="flex-start">
      {operation.isError ? <APIProblemView error={operation.error} /> : null}
      {operation.isPending ? <Text role="status">Loading launch progress…</Text> : null}
      {operation.data ? <WorkflowStepper operation={operation.data} failureTitle="Workspace launch failed" /> : null}
      <WorkspaceImageCache cache={workspace.isError ? undefined : workspace.data?.imageCache} />
      <Anchor href={`/workspaces/?id=${encodeURIComponent(accepted.workspaceId)}`}>View workspace</Anchor>
    </Stack>
  );
}
