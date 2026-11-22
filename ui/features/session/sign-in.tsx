import { Button, Group } from '@mantine/core';

export function SignInChoices() {
  return (
    <Group>
      <Button component="a" href="/auth/login">Sign in</Button>
      <Button component="a" href="/auth/external" variant="default">Sign in with your organization</Button>
    </Group>
  );
}
