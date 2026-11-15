import { useRef, useState } from 'react';
import { Alert, Button, Checkbox, Code, Group, Modal, Radio, Stack, Stepper, Text, TextInput } from '@mantine/core';
import { useForm } from '@mantine/form';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';

export function CreateLocalAccount({ csrfToken, refresh }: {
  csrfToken: string;
  refresh: () => Promise<unknown>;
}) {
  const [opened, setOpened] = useState(false);
  const [password, setPassword] = useState<string>();
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<unknown>();
  const member = useRef<HTMLInputElement>(null);
  const form = useForm({
    mode: 'uncontrolled',
    initialValues: { email: '', role: '' },
    validate: {
      email: value => /^[^\s@]+@[^\s@]+$/.test(value.trim()) ? null : 'Enter an email address.',
      role: value => value === 'member' || value === 'administrator' ? null : 'Choose a role.',
    },
  });

  const create = async ({ email, role }: typeof form.values) => {
    setError(undefined);
    try {
      const response = await fetch('/api/v1/admin/users', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
        body: JSON.stringify({ email: email.trim(), role }),
      });
      if (!response.ok) throw await readProblem(response);
      const result: { temporaryPassword?: string } = await response.json();
      if (!result.temporaryPassword) throw new Error('temporary password missing');
      form.reset();
      setPassword(result.temporaryPassword);
      await refresh();
    } catch (error) {
      setError(error);
    }
  };

  const canClose = !form.submitting && !password;
  const close = () => {
    setOpened(false);
    form.reset();
    setError(undefined);
  };
  return <>
    <Button onClick={() => setOpened(true)}>Add local account</Button>
    <Modal opened={opened} title="Create local account" onClose={canClose ? close : () => {}}
      closeOnEscape={canClose} closeOnClickOutside={false} withCloseButton={canClose} transitionProps={{ duration: 0 }}>
      <Stack>
        <Stepper active={password ? 1 : 0} size="sm" allowNextStepsSelect={false}>
          <Stepper.Step label="Account details" allowStepSelect={false} />
          <Stepper.Step label="Save password" allowStepSelect={false} />
        </Stepper>
        {password ? (
          <Stack align="flex-start">
            <Alert title="Temporary password" color="yellow">
              <Stack gap="xs">
                <Text size="sm">Save this password for private delivery. It is displayed once and must change at first sign-in.</Text>
                <Code block>{password}</Code>
              </Stack>
            </Alert>
            <Checkbox label="I saved the temporary password" checked={saved} onChange={event => setSaved(event.currentTarget.checked)} />
            <Button disabled={!saved} onClick={() => {
              setPassword(undefined);
              setSaved(false);
              close();
            }}>Done</Button>
          </Stack>
        ) : (
          <form noValidate onSubmit={form.onSubmit(create, errors => {
            if (errors.email) form.getInputNode('email')?.focus();
            else member.current?.focus();
          })}>
            <Stack maw={480} gap="sm">
              <Text size="sm" c="dimmed">Create an account with a temporary password to share privately.</Text>
              <TextInput label="Email address" type="email" autoComplete="off" data-autofocus required disabled={form.submitting}
                key={form.key('email')} {...form.getInputProps('email')} />
              <Radio.Group label="Role" withAsterisk key={form.key('role')} {...form.getInputProps('role')}>
                <Group mt="xs">
                  <Radio ref={member} value="member" label="Member" disabled={form.submitting} />
                  <Radio value="administrator" label="Administrator" disabled={form.submitting} />
                </Group>
              </Radio.Group>
              {error !== undefined ? <APIProblemView error={error} /> : null}
              <Group justify="flex-end">
                <Button variant="default" disabled={form.submitting} onClick={close}>Cancel</Button>
                <Button type="submit" loading={form.submitting}>Create local account</Button>
              </Group>
            </Stack>
          </form>
        )}
      </Stack>
    </Modal>
  </>;
}
