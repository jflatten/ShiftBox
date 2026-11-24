import { useEffect } from 'react';
import { Button, Chip, Group, Stack, Stepper, Text, TextInput, Title } from '@mantine/core';
import { useForm } from '@mantine/form';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';
import type { components } from '../../lib/api-types';

const endpoint = '/api/v1/admin/settings/github-app';
type SetupStatus = components['schemas']['GitHubAppSetupStatus'];
type Manifest = { action: string; manifest: string };
type SetupCommand =
  | { type: 'begin'; organization: string; restart: boolean }
  | { type: 'callback'; code: string; state: string }
  | { type: 'verify' };

const stateLabels: Record<SetupStatus['state'], string> = {
  not_configured: 'Setup required',
  registering: 'Registration started',
  exchanging: 'Completing registration',
  installation_pending: 'Repository access pending',
  ready: 'Ready',
  file_managed: 'Managed by configuration',
  recovery_required: 'Registration interrupted',
};

function githubURL(value: string, path: RegExp) {
  const url = new URL(value);
  if (url.origin !== 'https://github.com' || url.username || url.password || !path.test(url.pathname)) {
    throw new Error('The GitHub setup link is invalid.');
  }
  return url;
}

function postManifest(result: Manifest) {
  const target = githubURL(result.action, /^(?:\/organizations\/[A-Za-z0-9-]+)?\/settings\/apps\/new$/);
  const form = document.createElement('form');
  const manifest = document.createElement('input');
  form.action = target.href;
  form.method = 'post';
  form.hidden = true;
  manifest.type = 'hidden';
  manifest.name = 'manifest';
  manifest.value = result.manifest;
  form.append(manifest);
  document.body.append(form);
  try {
    form.submit();
  } finally {
    form.remove();
  }
}

export function GitHubAppSettings({ userId, csrfToken, active }: { userId: string; csrfToken: string; active: boolean }) {
  const queryClient = useQueryClient();
  const queryKey = ['github-app-setup', userId];
  const status = useQuery<SetupStatus>({
    queryKey,
    enabled: active,
    queryFn: async ({ signal }) => {
      const response = await fetch(endpoint, { signal, cache: 'no-store' });
      if (!response.ok) throw await readProblem(response);
      return response.json();
    },
    refetchInterval: query => query.state.data?.state === 'exchanging' ? 2000 : false,
  });
  const setup = useMutation({
    mutationFn: async (command: SetupCommand) => {
      const path = command.type === 'begin' ? '' : '/' + command.type;
      const body = command.type === 'begin' ? { organization: command.organization, restart: command.restart }
        : command.type === 'callback' ? { code: command.code, state: command.state } : undefined;
      const response = await fetch(endpoint + path, {
        method: 'POST', cache: 'no-store', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      if (!response.ok) throw await readProblem(response);
      if (command.type === 'begin') {
        postManifest(await response.json());
        return;
      }
      const result: SetupStatus = await response.json();
      queryClient.setQueryData(queryKey, result);
      if (command.type === 'callback' && result.installUrl) {
        const url = githubURL(result.installUrl, /^\/apps\/[a-z0-9-]+\/installations\/new$/);
        window.location.assign(url.href);
      }
    },
    onError: () => { void status.refetch(); },
  });
  const run = setup.mutate;
  const phase = status.data?.state;
  useEffect(() => {
    if (!active || !phase) return;
    const url = new URL(window.location.href);
    const fragment = new URLSearchParams(url.hash.slice(1));
    const code = fragment.get('github-code');
    const state = fragment.get('github-state');
    if (code && state) {
      url.hash = '';
      window.history.replaceState(window.history.state, '', url);
      run({ type: 'callback', code, state });
    } else if (url.searchParams.get('github-install') === '1') {
      url.searchParams.delete('github-install');
      url.searchParams.delete('installation_id');
      url.searchParams.delete('setup_action');
      window.history.replaceState(window.history.state, '', url);
      if (phase === 'installation_pending') run({ type: 'verify' });
    }
  }, [active, phase, run]);

  const form = useForm({
    mode: 'uncontrolled',
    initialValues: { organization: '' },
    validate: {
      organization: value => !value.trim() || /^[a-z0-9](?:[a-z0-9-]{0,37}[a-z0-9])?$/i.test(value.trim())
        ? null : 'Enter a GitHub organization name.',
    },
  });
  const begin = form.onSubmit(values => run({
    type: 'begin', organization: phase === 'registering' ? (status.data?.owner ?? '') : values.organization.trim(),
    restart: phase === 'recovery_required',
  }));
  const progress = phase === 'ready' ? 3 : phase === 'installation_pending' ? 1 : 0;
  const startable = phase === 'not_configured' || phase === 'registering' || phase === 'recovery_required';
  return (
    <Stack gap="md" align="flex-start">
      <Title order={2} id="github-app-settings-heading">GitHub App</Title>
      {status.isPending ? <Text role="status">Checking GitHub App setup…</Text> : null}
      {status.isError ? <>
        <APIProblemView error={status.error} />
        <Button variant="default" onClick={() => void status.refetch()}>Retry</Button>
      </> : null}
      {phase ? <Chip checked={phase === 'ready'} disabled variant="light">{stateLabels[phase]}</Chip> : null}
      {phase && phase !== 'file_managed' ? (
        <Stepper active={progress} size="xs" allowNextStepsSelect={false}>
          <Stepper.Step label="Register app" allowStepSelect={false} loading={phase === 'exchanging'} />
          <Stepper.Step label="Select repositories" allowStepSelect={false} loading={setup.isPending && phase === 'installation_pending'} />
          <Stepper.Step label="Ready for users" allowStepSelect={false} />
        </Stepper>
      ) : null}
      {status.data?.appName && status.data.owner ? <Text>{status.data.appName} · {status.data.owner}</Text> : null}
      {phase === 'file_managed' ? <Text size="sm">This controller uses an app supplied by its server configuration.</Text> : null}
      {phase === 'ready' ? <Text role="status">Workspace users can now connect their GitHub accounts.</Text> : null}
      {phase === 'installation_pending' ? (
        <Text size="sm">Select repositories on GitHub. If your organization requires approval, check again after it is granted.</Text>
      ) : null}
      {phase === 'recovery_required' ? (
        <Text size="sm">Check your GitHub App settings for the previous registration before starting over.</Text>
      ) : null}
      {setup.isError ? <APIProblemView error={setup.error} /> : null}
      {startable ? (
        <form onSubmit={begin}>
          <Stack align="flex-start">
            {phase !== 'registering' ? (
              <TextInput label="GitHub organization" description="Leave blank to use your personal GitHub account."
                key={form.key('organization')} {...form.getInputProps('organization')} />
            ) : null}
            <Button type="submit" loading={setup.isPending}>
              {phase === 'recovery_required' ? 'Start over' : phase === 'registering' ? 'Continue registration' : 'Set up GitHub App'}
            </Button>
          </Stack>
        </form>
      ) : null}
      {phase === 'installation_pending' && status.data?.installUrl ? (
        <Group>
          <Button component="a" href={status.data.installUrl}>Select repositories on GitHub</Button>
          <Button variant="default" loading={setup.isPending} onClick={() => run({ type: 'verify' })}>Check installation</Button>
        </Group>
      ) : null}
      {phase === 'ready' && status.data?.manageUrl ? (
        <Button component="a" href={status.data.manageUrl} variant="default">Manage repository access on GitHub</Button>
      ) : null}
    </Stack>
  );
}
