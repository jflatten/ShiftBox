import { useEffect, useRef, useState } from 'react';
import { Button, Group, Modal, Stack, Stepper, Text, Title } from '@mantine/core';
import { useQuery } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';
import { readAuthorizationLink } from '../../lib/authorization-link';
import { useAuthorizationExpired } from '../../lib/use-authorization-expired';

const endpoint = '/api/v1/git/authorization';
const stateLabels = {
  connected: 'GitHub is connected.',
  refreshing: 'Refreshing GitHub authorization…',
  reconnect_required: 'Reconnect your GitHub account to restore workspace Git access.',
  revoked: 'GitHub is disconnected.',
};

export function GitConnection({ userID, csrfToken }: { userID: string; csrfToken: string }) {
  const status = useQuery<{ state: keyof typeof stateLabels }>({
    queryKey: ['git-authorization', userID],
    queryFn: async ({ signal }) => {
      const response = await fetch(endpoint, { cache: 'no-store', signal });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
    refetchInterval: query => query.state.data?.state === 'refreshing' ? 3000 : false,
  });
  const [link, setLink] = useState<ReturnType<typeof readAuthorizationLink>>();
  const [pending, setPending] = useState<'POST' | 'DELETE'>();
  const [error, setError] = useState<unknown>();
  const [invalidLink, setInvalidLink] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const next = useRef<HTMLAnchorElement>(null);
  const expired = useAuthorizationExpired(link?.expiresAt ?? NaN);
  const ready = link !== undefined && !expired;
  useEffect(() => { if (ready) next.current?.focus(); }, [ready]);

  async function change(method: 'POST' | 'DELETE') {
    if (pending) return;
    setPending(method);
    setError(undefined);
    setInvalidLink(false);
    setLink(undefined);
    try {
      const response = await fetch(endpoint, { method, headers: { 'X-CSRF-Token': csrfToken }, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      if (method === 'DELETE') {
        setConfirm(false);
        await status.refetch();
      } else {
        const result = readAuthorizationLink(await response.json(), 'https://github.com', '/login/oauth/authorize');
        if (result) setLink(result);
        else setInvalidLink(true);
      }
    } catch (error) {
      setError(error);
    } finally {
      setPending(undefined);
    }
  }

  const state = !status.isError ? status.data?.state : undefined;
  const canConnect = state === 'revoked' || state === 'reconnect_required';
  const canDisconnect = state === 'connected' || state === 'refreshing' || state === 'reconnect_required';
  return (
    <Stack align="flex-start" gap="sm">
      <Title order={2}>GitHub</Title>
      {status.isPending ? <Text role="status">Checking GitHub authorization…</Text> : null}
      {status.isError ? <>
        <APIProblemView error={status.error} />
        <Button variant="default" onClick={() => void status.refetch()}>Retry</Button>
      </> : null}
      {state ? <Text role="status">{stateLabels[state]}</Text> : null}
      {canConnect ? (
        <Stepper active={ready ? 1 : 0} size="xs" allowNextStepsSelect={false}>
          <Stepper.Step label="Start connection" allowStepSelect={false} loading={pending === 'POST'} />
          <Stepper.Step label="Authorize on GitHub" allowStepSelect={false} />
        </Stepper>
      ) : null}
      {(invalidLink || expired) ? <Text role="alert">The GitHub authorization link is unavailable. Try again.</Text> : null}
      {!confirm && error !== undefined ? <APIProblemView error={error} /> : null}
      <Group>
        {canConnect && ready ? <Button component="a" href={link.href} ref={next}>Continue to GitHub</Button> : null}
        {canConnect && !ready ? <Button loading={pending === 'POST'} disabled={!!pending} onClick={() => void change('POST')}>
          {state === 'reconnect_required' ? 'Reconnect GitHub' : 'Connect GitHub'}
        </Button> : null}
        {canDisconnect ? <Button variant="default" disabled={!!pending} onClick={() => {
          setError(undefined);
          setConfirm(true);
        }}>Disconnect GitHub</Button> : null}
      </Group>
      {confirm ? (
        <Modal opened title="Disconnect GitHub" closeButtonProps={{ 'aria-label': 'Close disconnect confirmation' }} onClose={() => setConfirm(false)} closeOnClickOutside={!pending}
          closeOnEscape={!pending} withCloseButton={!pending} transitionProps={{ duration: 0 }}>
          <Stack>
            <Text>Workspaces will stop receiving your GitHub credentials until you reconnect.</Text>
            {error !== undefined ? <APIProblemView error={error} /> : null}
            <Group justify="flex-end">
              <Button variant="default" disabled={!!pending} onClick={() => setConfirm(false)} data-autofocus>Cancel</Button>
              <Button color="red" loading={pending === 'DELETE'} onClick={() => void change('DELETE')}>Confirm disconnect</Button>
            </Group>
          </Stack>
        </Modal>
      ) : null}
    </Stack>
  );
}
