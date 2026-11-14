import { MantineProvider } from '@mantine/core';
import { render, screen } from '@testing-library/react';
import { ShiftBoxShell } from './app-shell';

// A member must not be offered administration navigation by the shared shell.
test('shows member navigation and the approved brand', () => {
  render(<MantineProvider><ShiftBoxShell session={{ role: 'member', mode: 'normal', installationState: 'active', permissions: ['template.create'] }} alerts={[]}><h1>Workspaces</h1></ShiftBoxShell></MantineProvider>);
  expect(screen.getByRole('link', { name: 'Workspaces' })).toBeVisible();
  expect(screen.queryByRole('link', { name: 'Infrastructure' })).not.toBeInTheDocument();
  expect(screen.getByAltText('ShiftBox')).toHaveAttribute('src', '/brand/shiftbox-logo-light.svg');
});

// Restricted administrator sessions must not retain the navigation of a normal active session.
test('respects session gates and keeps persistent conditions visible', () => {
  render(<MantineProvider><ShiftBoxShell session={{ role: 'administrator', mode: 'password_change', installationState: 'active', permissions: ['session.change_password'] }} alerts={[{ id: 'password', title: 'Change your password', detail: 'Choose a new password to continue.' }]}><h1>Account</h1></ShiftBoxShell></MantineProvider>);
  expect(screen.queryByRole('link', { name: 'Workspaces' })).not.toBeInTheDocument();
  expect(screen.queryByRole('link', { name: 'Infrastructure' })).not.toBeInTheDocument();
  expect(screen.getByRole('alert')).toHaveTextContent('Choose a new password to continue.');
  expect(screen.getByRole('region', { name: 'Persistent conditions' })).toContainElement(screen.getByRole('alert'));
  expect(screen.queryByRole('button', { name: /close/i })).not.toBeInTheDocument();
});

// Administrative navigation needs the server's current permission, not the role name alone.
test('shows permitted administration navigation', () => {
  render(<MantineProvider><ShiftBoxShell session={{ role: 'administrator', mode: 'normal', installationState: 'active', permissions: ['infrastructure.view'] }} alerts={[]}><h1>Overview</h1></ShiftBoxShell></MantineProvider>);
  expect(screen.getByRole('link', { name: 'Infrastructure' })).toBeVisible();
  expect(screen.queryByRole('link', { name: 'Users' })).not.toBeInTheDocument();
});
