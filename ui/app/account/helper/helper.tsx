'use client';

import { useMutation } from '@tanstack/react-query';
import { Alert, Button, Chip, Group, Paper, Stack, Stepper, Text, Title } from '@mantine/core';
import { APIProblemView } from '../../../components/api-problem';
import { ShiftBoxShell } from '../../../components/app-shell';
import { readControllerJSON } from '../../../lib/api-problem';
import { useSession } from '../../../lib/use-session';
import type { operations } from '../../../lib/api-types';

type Approval = operations['approveWorkspaceSSHHelper']['requestBody']['content']['application/json'];
type ApprovalResponse = operations['approveWorkspaceSSHHelper']['responses'][200]['content']['application/json'];

const proofPattern = /^[A-Za-z0-9_-]{43}$/;
const statePattern = /^[A-Za-z0-9._~-]{22,128}$/;
const redirectPattern = /^http:\/\/127\.0\.0\.1:([1-9][0-9]{0,4})\/callback$/;

function parseApproval(query: string): Approval | null {
  const params = new URLSearchParams(query);
  const state = params.get('state') ?? '';
  const challenge = params.get('challenge') ?? '';
  const redirectUri = params.get('redirectUri') ?? '';
  const port = redirectPattern.exec(redirectUri)?.[1];
  if (!statePattern.test(state) || !proofPattern.test(challenge) || !port || Number(port) > 65535 ||
      ['state', 'challenge', 'redirectUri'].some(name => params.getAll(name).length !== 1)) return null;
  return { state, challenge, redirectUri };
}

class InvalidCallback extends Error {}

function verifyCallback(value: string, request: Approval) {
  if (typeof value !== 'string' || !value.startsWith(request.redirectUri + '?')) throw new InvalidCallback();
  const callback = new URL(value);
  const params = callback.searchParams;
  if (callback.hash || params.size !== 2 || params.get('state') !== request.state ||
      !proofPattern.test(params.get('code') ?? '')) throw new InvalidCallback();
  return callback.href;
}

function openCallback(url: string) {
  window.location.assign(url);
}

export function LocalHelper({ query, onReturn = openCallback }: { query: string; onReturn?: (url: string) => void }) {
  const session = useSession();
  const current = session.session;
  const eligible = current?.mode === 'normal' && current.installationState === 'active' &&
    current.permissions.includes('workspace.list');
  const pairing = query !== '';
  const request = pairing ? parseApproval(query) : null;
  const approval = useMutation({
    mutationFn: async ({ input, csrfToken }: { input: Approval; csrfToken: string }) => {
      const response = await readControllerJSON<ApprovalResponse>('/api/v1/ssh/helper/approve', {
        method: 'POST',
        cache: 'no-store',
        redirect: 'error',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
        body: JSON.stringify(input),
      });
      return verifyCallback(response.callbackUrl, input);
    },
    onSuccess: url => onReturn(url),
  });
  const step = approval.isSuccess ? 3 : eligible ? (approval.isPending ? 2 : 1) : 0;

  return (
    <ShiftBoxShell session={current} alerts={[]} error={session.error ?? undefined}>
      <Stack gap="lg" maw={760}>
        <Title order={1}>{pairing ? 'Pair Local Helper' : 'Install Local Helper'}</Title>
        {pairing && !request ? <PairingError /> : <>
          {pairing ? <Stepper active={step} allowNextStepsSelect={false} orientation="vertical" size="sm"
            aria-label="Pairing progress">
            <Stepper.Step label="Sign in to ShiftBox" allowStepSelect={false} />
            <Stepper.Step label="Review the account and approve" allowStepSelect={false} />
            <Stepper.Step label="Return to the local helper" allowStepSelect={false} loading={approval.isPending} />
          </Stepper> : null}

          {session.isPending ? <Text role="status">Checking your session…</Text> : null}
          {session.isError ? <Group><Button variant="default" onClick={() => void session.refetch()}>Retry session check</Button></Group> : null}
          {session.data === null ? <Paper withBorder radius="md" p="lg">
            <Stack>
              <Text>{pairing ? 'Sign in in a new tab, then return here to approve pairing.' : 'Sign in to download the internal macOS helper.'}</Text>
              <Group>
                <Button component="a" href="/auth/login" target={pairing ? '_blank' : undefined} rel={pairing ? 'noopener noreferrer' : undefined}>Sign in</Button>
                <Button component="a" href="/auth/external" target={pairing ? '_blank' : undefined}
                  rel={pairing ? 'noopener noreferrer' : undefined} variant="default">Sign in with your organization</Button>
                {pairing ? <Button variant="subtle" onClick={() => void session.refetch()}>Check sign-in</Button> : null}
              </Group>
            </Stack>
          </Paper> : null}
          {current && !eligible ? <Alert color="blue" title="Workspace access is required">
            <Button component="a" href="/account/" variant="default" mt="sm">Open personal settings</Button>
          </Alert> : null}
          {eligible && current && !pairing ? <Paper withBorder radius="md" p="lg">
            <Stack gap="md">
              <Text>This internal build is for the private macOS pilot.</Text>
              <Group><Button component="a" href="/api/v1/ssh/helper/download" download>Download for macOS</Button></Group>
              <Text size="sm">Open the disk image and drag ShiftBox Local Helper to Applications. Open it from Applications and follow the browser prompts to pair it with this installation.</Text>
              <Text size="sm">Approve the login item in System Settings to keep your SSH aliases up to date after you sign in to macOS. If macOS blocks the app, contact your ShiftBox administrator.</Text>
            </Stack>
          </Paper> : null}

          {eligible && current && request ? <Paper component="section" aria-label="Pairing details" withBorder radius="md" p="lg">
            <Stack gap="md">
              <Stack gap={4}>
                <Title order={2} size="h3">Installation</Title>
                <Text style={{ overflowWrap: 'anywhere' }}>{window.location.origin}</Text>
              </Stack>
              <Stack gap={4}>
                <Title order={2} size="h3">Account</Title>
                {current.user.displayName ? <Text>{current.user.displayName}</Text> : null}
                <Text style={{ overflowWrap: 'anywhere' }}>{current.user.primaryEmail ?? (current.user.displayName ? null : 'Your signed-in account')}</Text>
              </Stack>
              <Text size="sm">Approve only if you opened ShiftBox Local Helper on this Mac. The app will be able to discover and connect to workspaces available to this account.</Text>
              {approval.error instanceof InvalidCallback ? <PairingError /> : approval.error ? <APIProblemView error={approval.error} /> : null}
              <Group>
                {approval.isSuccess ? <span role="status">
                  <Chip checked readOnly hidden icon={false} size="sm">Approval sent</Chip>
                </span> : <Button loading={approval.isPending} onClick={() => approval.mutate({ input: request, csrfToken: current.csrfToken })}>
                  Approve pairing
                </Button>}
              </Group>
            </Stack>
          </Paper> : null}
        </>}
      </Stack>
    </ShiftBoxShell>
  );
}

function PairingError() {
  return <Alert color="red" title="Pairing request is invalid">Restart pairing from the macOS helper.</Alert>;
}
