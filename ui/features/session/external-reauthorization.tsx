import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Stack, Stepper, Text } from '@mantine/core';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';
import { readAuthorizationLink } from '../../lib/authorization-link';

import { useAuthorizationExpired } from '../../lib/use-authorization-expired';

export function ExternalReauthorization({ csrfToken, dueAt, expired }: {
  csrfToken: string;
  dueAt?: string | null;
  expired: boolean;
}) {
  const [link, setLink] = useState<ReturnType<typeof readAuthorizationLink>>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  const [invalidLink, setInvalidLink] = useState(false);
  const next = useRef<HTMLAnchorElement>(null);
  const linkExpired = useAuthorizationExpired(link?.expiresAt ?? NaN);
  const ready = link !== undefined && !linkExpired;
  useEffect(() => { if (ready) next.current?.focus(); }, [ready]);

  async function renew() {
    if (pending) return;
    setPending(true);
    setError(undefined);
    setLink(undefined);
    setInvalidLink(false);
    try {
      const response = await fetch('/api/v1/session/external-reauthorization', {
        method: 'POST', headers: { 'X-CSRF-Token': csrfToken }, cache: 'no-store',
      });
      if (!response.ok) throw await readProblem(response);
      const result = readAuthorizationLink(await response.json(), window.location.origin, '/dex/auth');
      if (!result) {
        setInvalidLink(true);
        return;
      }
      setLink(result);
    } catch (error) {
      setError(error);
    } finally {
      setPending(false);
    }
  }

  const due = Date.parse(dueAt ?? '');
  return (
    <Alert title={expired ? 'Organizational authorization expired' : 'Organizational authorization'} color={expired ? 'yellow' : 'violet'}>
      <Stack gap="sm" align="flex-start">
        {Number.isFinite(due) ? (
          <Text size="sm">Due <time dateTime={dueAt ?? undefined}>{new Date(due).toLocaleString()}</time></Text>
        ) : null}
        <Stepper active={ready ? 1 : 0} size="xs" allowNextStepsSelect={false}>
          <Stepper.Step label="Request renewal" allowStepSelect={false} loading={pending} />
          <Stepper.Step label="Authorize with your organization" allowStepSelect={false} />
        </Stepper>
        {error !== undefined ? <APIProblemView error={error} /> : null}
        {invalidLink || linkExpired ? <Text role="alert" size="sm">The authorization link is unavailable. Try again.</Text> : null}
        {ready ? (
          <Button component="a" href={link.href} ref={next}>Continue to organization</Button>
        ) : (
          <Button onClick={() => void renew()} loading={pending}>Renew authorization</Button>
        )}
      </Stack>
    </Alert>
  );
}
