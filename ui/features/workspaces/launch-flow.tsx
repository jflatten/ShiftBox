import { useRef, useState, type ReactNode } from 'react';
import { useRouter } from 'next/navigation';
import { Button, Group, Modal, SimpleGrid, Stack, Text, TextInput, Title } from '@mantine/core';
import { APIProblemView } from '../../components/api-problem';
import type { operations } from '../../lib/api-types';
import { readProblem } from '../../lib/api-problem';
import { providerLabel } from '../../lib/provider-label';
import type { TemplateRevision } from '../templates/use-templates';
import { markWorkspaceLaunch, workspaceURL } from './workspace';

type AcceptedLaunch = operations['launchWorkspace']['responses'][202]['content']['application/json'];

export function LaunchFlow({ csrfToken, children }: {
  csrfToken: string;
  children: (onReview: (project: TemplateRevision) => void) => ReactNode;
}) {
  const router = useRouter();
  const [name, setName] = useState('');
  const [review, setReview] = useState<TemplateRevision>();
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
      const { workspaceId }: AcceptedLaunch = await response.json();
      if (!workspaceId) throw new Error('The launch response did not include a workspace ID.');
      markWorkspaceLaunch(workspaceId);
      router.push(workspaceURL(workspaceId));
    } catch (error) {
      setError(error);
      setSubmitting(false);
    }
  };

  return (
    <>
      {children(setReview)}
      <Modal opened={!!review} onClose={closeReview} title="Review launch" centered size="lg" padding="lg"
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
