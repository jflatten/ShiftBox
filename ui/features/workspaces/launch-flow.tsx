import { useRef, useState } from 'react';
import { Anchor, Button, Group, Radio, Stack, Text, Title } from '@mantine/core';
import { APIProblemView } from '../../components/api-problem';
import { WorkflowStepper } from '../../components/workflow-stepper';
import { readProblem } from '../../lib/api-problem';
import { useOperation } from '../../lib/use-operation';
import { useTemplates, type TemplateRevision } from '../templates/use-templates';

interface AcceptedLaunch {
  id: string;
  workspaceId: string;
}

export function LaunchFlow({ csrfToken }: { csrfToken: string }) {
  const [cursors, setCursors] = useState<string[]>([]);
  const [selected, setSelected] = useState('');
  const [review, setReview] = useState<TemplateRevision>();
  const [accepted, setAccepted] = useState<AcceptedLaunch>();
  const [error, setError] = useState<unknown>();
  const [submitting, setSubmitting] = useState(false);
  const requestKeys = useRef(new Map<string, string>());
  const cursor = cursors.at(-1);
  const templates = useTemplates(cursor, !accepted);
  const data = templates.isError ? undefined : templates.data;
  const chosen = data?.items.find(item => templateKey(item) === selected);

  const begin = async () => {
    if (!review || submitting) return;
    setSubmitting(true);
    setError(undefined);
    try {
      const revision = templateKey(review);
      const key = requestKeys.current.get(revision) ?? crypto.randomUUID();
      requestKeys.current.set(revision, key);
      const response = await fetch('/api/v1/workspaces', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'Idempotency-Key': key },
        body: JSON.stringify({ templateId: review.id, templateRevision: review.currentRevision }),
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
      {accepted ? <LaunchProgress accepted={accepted} /> : review ? (
        <Stack>
          <Title order={2}>{review.name} · Revision {review.currentRevision}</Title>
          <dl>
            <Text component="dt" size="sm" c="dimmed">Repository</Text>
            <Text component="dd" ml={0} mb="sm" style={{ overflowWrap: 'anywhere' }}>{review.repositoryUrl}</Text>
            {review.repositoryRef ? <>
              <Text component="dt" size="sm" c="dimmed">Repository ref</Text>
              <Text component="dd" ml={0} mb="sm">{review.repositoryRef}</Text>
            </> : null}
            <Text component="dt" size="sm" c="dimmed">Dev Container configuration</Text>
            <Text component="dd" ml={0} mb="sm">{review.devcontainerPath}</Text>
            <Text component="dt" size="sm" c="dimmed">Resource profile</Text>
            <Text component="dd" ml={0} mb="sm">{review.resourceProfileId}</Text>
            <Text component="dt" size="sm" c="dimmed">Platform image</Text>
            <Text component="dd" ml={0} mb="sm">{review.platformArtifactId}</Text>
            <Text component="dt" size="sm" c="dimmed">Secret references</Text>
            <Text component="dd" ml={0}>{review.secretNames?.join(', ') || 'None'}</Text>
          </dl>
          {error !== undefined ? <APIProblemView error={error} /> : null}
          <Group>
            <Button loading={submitting} onClick={() => void begin()}>Launch workspace</Button>
            <Button variant="default" disabled={submitting} onClick={() => {
              setReview(undefined);
              setError(undefined);
            }}>Back to templates</Button>
          </Group>
        </Stack>
      ) : (
        <Stack align="flex-start">
          {templates.isError ? <APIProblemView error={templates.error} /> : null}
          {templates.isPending ? <Text role="status">Loading templates…</Text> : null}
          {data?.items.length === 0 ? <Text>No templates on this page.</Text> : null}
          {!!data?.items.length ? (
            <Radio.Group label="Choose a template" value={selected} onChange={setSelected}>
              <Stack gap="sm" mt="sm">
                {data.items.map(item => <Radio key={templateKey(item)} value={templateKey(item)}
                  label={`${item.name} · Revision ${item.currentRevision}`} />)}
              </Stack>
            </Radio.Group>
          ) : null}
          {cursor || data?.nextCursor ? (
            <Group>
              <Button variant="default" disabled={!cursor} onClick={() => setCursors(current => current.slice(0, -1))}>Previous page</Button>
              <Button variant="default" disabled={!data?.nextCursor} onClick={() => {
                const next = data?.nextCursor;
                if (next) setCursors(current => [...current, next]);
              }}>Next page</Button>
            </Group>
          ) : null}
          {data?.items.length ? <Button disabled={!chosen} onClick={() => {
            if (!chosen) return;
            setReview(chosen);
          }}>Review launch</Button> : null}
        </Stack>
      )}
    </>
  );
}

function templateKey(template: TemplateRevision) {
  return `${template.id}/${template.currentRevision}`;
}

function LaunchProgress({ accepted }: { accepted: AcceptedLaunch }) {
  const operation = useOperation(accepted.id);
  if (operation.accessDenied) return <Text role="alert">You no longer have access to this operation.</Text>;
  return (
    <Stack align="flex-start">
      {operation.isError ? <APIProblemView error={operation.error} /> : null}
      {operation.isPending ? <Text role="status">Loading launch progress…</Text> : null}
      {operation.data ? <WorkflowStepper operation={operation.data} /> : null}
      <Anchor href={`/workspaces/?id=${encodeURIComponent(accepted.workspaceId)}`}>View workspace</Anchor>
    </Stack>
  );
}
