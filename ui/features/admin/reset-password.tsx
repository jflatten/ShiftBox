import { useState } from 'react';
import { Button, Checkbox, Code, Group, Modal, Stack, Stepper, Text } from '@mantine/core';
import { APIProblemView } from '../../components/api-problem';
import { readProblem } from '../../lib/api-problem';

export function ResetPassword({ person, csrfToken, onClose, refresh }: {
  person: { id: string; primaryEmail: string; version: number };
  csrfToken: string;
  onClose: () => void;
  refresh: () => Promise<unknown>;
}) {
  const [password, setPassword] = useState<string>();
  const [saved, setSaved] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>();
  const canClose = !saving && !password;

  const reset = async () => {
    if (saving || password || error !== undefined) return;
    setSaving(true);
    try {
      const response = await fetch(`/api/v1/admin/users/${encodeURIComponent(person.id)}/password-reset`, {
        method: 'POST',
        cache: 'no-store',
        headers: { 'X-CSRF-Token': csrfToken, 'If-Match': `"${person.version}"` },
      });
      if (!response.ok) throw await readProblem(response);
      const result: { temporaryPassword?: string } = await response.json();
      if (!result.temporaryPassword) throw new Error('temporary password missing');
      setPassword(result.temporaryPassword);
    } catch (error) {
      setError(error);
      void refresh();
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal opened returnFocus={false} title="Reset password" onClose={canClose ? onClose : () => {}}
      closeOnEscape={canClose} closeOnClickOutside={canClose} withCloseButton={canClose}
      transitionProps={{ duration: 0 }}>
      <Stack>
        <Text fw={600}>{person.primaryEmail}</Text>
        <Stepper active={password ? 1 : 0} size="sm" allowNextStepsSelect={false}>
          <Stepper.Step label="Confirm reset" allowStepSelect={false} />
          <Stepper.Step label="Save password" allowStepSelect={false} />
        </Stepper>
        {password ? (
          <>
            <Text size="sm">Save this temporary password for private delivery. The account holder must change it at sign-in.</Text>
            <Code block>{password}</Code>
            <Checkbox label="I saved the temporary password" checked={saved} onChange={event => setSaved(event.currentTarget.checked)} />
            <Group justify="flex-end">
              <Button disabled={!saved} onClick={() => {
                onClose();
                void refresh();
              }}>Done</Button>
            </Group>
          </>
        ) : (
          <>
            <Text size="sm">Resetting the password signs this account out and closes its active browser connections.</Text>
            {error !== undefined ? <APIProblemView error={error} /> : null}
            <Group justify="flex-end">
              <Button variant="default" disabled={saving} data-autofocus onClick={onClose}>{error === undefined ? 'Cancel' : 'Close'}</Button>
              {error === undefined ? (
                <Button loading={saving} onClick={() => void reset()}>Generate temporary password</Button>
              ) : null}
            </Group>
          </>
        )}
      </Stack>
    </Modal>
  );
}
