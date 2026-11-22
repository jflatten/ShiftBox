import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Providers } from '../../app/providers';
import { BrowserCertificate } from './browser-certificate';

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

const certificate = {
  subject: 'CN=management.test,O=ShiftBox', issuer: 'CN=Example CA',
  dnsNames: ['management.test', '*.ide.test', '*.preview.test'],
  ipAddresses: ['192.0.2.1'], emailAddresses: ['admin@example.test'], uris: ['https://example.test/certificate'],
  notBefore: '2026-11-01T12:00:00Z', notAfter: '2027-11-01T12:00:00Z',
  serialNumber: 'ab0123', fingerprintSha256: 'abc123'.repeat(10) + 'abcd',
  signatureAlgorithm: 'ECDSA-SHA256', publicKeyAlgorithm: 'ECDSA', version: 3, chainLength: 2, status: 'valid',
};

// Administrators need the installed identity and all SAN types, exact UTC
// validity dates, and identifying fields without opening a certificate file.
test('shows installed certificate details above the upload form', async () => {
  globalThis.fetch = jest.fn(async () => ({ ok: true, json: async () => certificate }) as Response);
  render(<Providers><BrowserCertificate userId="admin" active csrfToken="fixture" refresh={async () => {}} /></Providers>);
  const details = await screen.findByRole('region', { name: 'Installed certificate' });
  expect(details).toHaveClass('mantine-Card-root');
  for (const value of [certificate.subject, certificate.issuer, ...certificate.dnsNames, ...certificate.ipAddresses,
    ...certificate.emailAddresses, ...certificate.uris, certificate.serialNumber, certificate.fingerprintSha256,
    certificate.signatureAlgorithm, certificate.publicKeyAlgorithm]) {
    expect(within(details).getByText(value)).toBeVisible();
  }
  expect(within(details).getByText('Valid')).toBeVisible();
  expect(within(details).getByText('2027-11-01 12:00:00 UTC')).toBeVisible();
  expect(within(details).getByText('2026-11-01 12:00:00 UTC')).toBeVisible();
  expect(within(details).getByText('2 certificates')).toBeVisible();
  expect(screen.getByRole('button', { name: 'Replace certificate' })).toBeEnabled();
});

// A failed metadata request should have an explicit retry path and must not
// prevent an administrator from uploading a replacement certificate.
test('retries a failed detail read and shows an expiry warning', async () => {
  let unavailable = true;
  globalThis.fetch = jest.fn(async () => unavailable ? new Response(null, { status: 503 })
    : ({ ok: true, json: async () => ({ ...certificate, status: 'expiring' }) }) as Response);
  render(<Providers><BrowserCertificate userId="admin" active csrfToken="fixture" refresh={async () => {}} /></Providers>);
  expect(screen.getByText('Loading certificate details…')).toBeVisible();
  const retry = await screen.findByRole('button', { name: 'Retry certificate details' });
  expect(screen.getByRole('button', { name: 'Replace certificate' })).toBeEnabled();
  unavailable = false;
  await userEvent.setup().click(retry);
  expect(await screen.findByText('Expires soon')).toBeVisible();
  expect(screen.queryByRole('button', { name: 'Retry certificate details' })).not.toBeInTheDocument();
});
