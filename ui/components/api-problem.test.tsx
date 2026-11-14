import { MantineProvider } from '@mantine/core';
import { render, screen } from '@testing-library/react';
import { APIProblemView } from './api-problem';
import { APIProblemError } from '../lib/api-problem';

// Failure guidance remains visible, including field corrections and a support reference.
test('renders structured recovery guidance as a persistent alert', () => {
  const error = new APIProblemError({ type: 'about:blank', status: 409, code: 'request.conflict', title: 'Refresh before retrying', detail: 'The workspace has changed.', requestId: 'reference-one', fields: { name: 'Choose another name.' } });
  render(<MantineProvider><APIProblemView error={error} /></MantineProvider>);
  expect(screen.getByRole('alert')).toHaveTextContent('Refresh before retrying');
  expect(screen.getByRole('alert')).toHaveTextContent('Choose another name.');
  expect(screen.getByRole('alert')).toHaveTextContent('reference-one');
  expect(screen.queryByRole('button')).not.toBeInTheDocument();
});

// Unexpected network/library exceptions must not expose raw messages to the product UI.
test('does not display arbitrary exception strings', () => {
  render(<MantineProvider><APIProblemView error={new Error('private access token')} /></MantineProvider>);
  expect(screen.getByRole('alert')).toHaveTextContent('Unable to complete the request');
  expect(screen.queryByText(/private access token/)).not.toBeInTheDocument();
});
