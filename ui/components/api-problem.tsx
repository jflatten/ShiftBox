import { Alert, Stack, Text } from '@mantine/core';
import { APIProblemError, fallbackProblem } from '../lib/api-problem';

/** Persistent recovery guidance; retrying a mutation remains an explicit caller action. */
export function APIProblemView({ error }: { error: unknown }) {
  const problem = error instanceof APIProblemError ? error.problem : fallbackProblem();
  return (
    <Alert color="red" title={problem.title}>
      <Stack gap="xs">
        {problem.detail ? <Text size="sm">{problem.detail}</Text> : null}
        {Object.entries(problem.fields ?? {}).map(([field, message]) => <Text key={field} size="sm">{field}: {message}</Text>)}
        {problem.requestId ? <Text size="xs">Request reference: {problem.requestId}</Text> : null}
      </Stack>
    </Alert>
  );
}
