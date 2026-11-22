import { expect, test } from '@playwright/test';

const backupID = '42c5a1d9-fc7a-4fc7-9905-0037e6bf0061';
const restoreID = '42c5a1d9-fc7a-4fc7-9905-0037e6bf0062';
const validationID = '42c5a1d9-fc7a-4fc7-9905-0037e6bf0063';
const reviewID = '42c5a1d9-fc7a-4fc7-9905-0037e6bf0064';
const archive = '/private/shiftbox-test.age';
const passphrase = 'disposable browser test passphrase';
const ownerURI = 'postgres://owner:fixture@db.example.test/shiftbox?sslmode=verify-full';
const dexURI = 'postgres://dex:fixture@db.example.test/shiftbox?sslmode=verify-full';
const administrator = { id: 'admin', role: 'administrator' };

// Exercise the exported UI with in-browser API fixtures. T116 verifies the
// controller, protected local restore command, and recovery listener together.
test.use({ trace: 'off', screenshot: 'off', video: 'off' });

// A keyboard submission keeps the passphrase out of visible state. Progress
// follows the server's step indexes and disappears when authority is revoked.
test('backs up and validates a restore through separate workflows', async ({ page }) => {
  let backupWrites = 0;
  let restoreWrites = 0;
  let revoked = false;
  const backup = {
    id: backupID, status: 'running', currentStep: 'checkpoint', version: 2,
    steps: [
      { key: 'encrypt', index: 1, label: 'Encrypt and verify the archive', status: 'pending' },
      { key: 'checkpoint', index: 0, label: 'Capture management and identity data', status: 'active' },
    ],
  };
  const restore = {
    id: restoreID, status: 'succeeded', version: 2,
    steps: [
      { key: 'validate', index: 0, label: 'Verify archive and replacement', status: 'succeeded', detail: validationID },
    ],
  };
  await page.route('**/api/v1/**', async route => {
    const path = new URL(route.request().url()).pathname;
    switch (path) {
      case '/api/v1/session':
        return route.fulfill({ json: {
          user: administrator, mode: 'normal', installationState: 'active',
          permissions: ['backup.create', 'restore.validate'], csrfToken: 'normal-fixture',
        } });
      case '/api/v1/backups':
        expect(route.request().method()).toBe('POST');
        expect(route.request().postDataJSON()).toEqual({ destination: archive, password: passphrase });
        backupWrites++;
        return route.fulfill({ status: 202, json: backup });
      case '/api/v1/restores/validate':
        expect(route.request().method()).toBe('POST');
        expect(route.request().postDataJSON()).toEqual({
          archivePath: archive, destination: '/private/replacement', password: passphrase,
          replacementPostgres: { profile: 'postgres', tlsMode: 'verify-full', connectionUri: ownerURI, dexConnectionUri: dexURI },
        });
        restoreWrites++;
        return route.fulfill({ status: 202, json: restore });
      case `/api/v1/operations/${backupID}`:
        return route.fulfill(revoked ? { status: 403, json: {} } : { json: backup });
      case `/api/v1/operations/${backupID}/events`:
        return route.fulfill({ contentType: 'text/event-stream', body: revoked
          ? 'event: authorization.revoked\ndata: {}\n\n' : 'retry: 200\n\n' });
      case `/api/v1/operations/${restoreID}`:
        return route.fulfill({ json: restore });
      default:
        return route.fulfill({ status: 404, json: {} });
    }
  });

  await page.goto('/recovery/');
  await expect(page.getByText(/workspace VM disks and files stay outside the archive/i)).toBeVisible();
  const backupSection = page.locator('section').filter({ has: page.getByRole('heading', { name: 'Backup', exact: true }) });
  const destination = backupSection.getByLabel('Destination');
  const backupPassphrase = backupSection.getByLabel('Passphrase');
  const create = backupSection.getByRole('button', { name: 'Create backup' });
  await create.press('Enter');
  await expect(destination).toBeFocused();
  expect(backupWrites).toBe(0);
  await destination.fill(archive);
  await backupPassphrase.fill(passphrase);
  await create.press('Enter');
  await expect(backupPassphrase).toHaveValue('');
  const checkpoint = backupSection.getByRole('button', { name: 'Capture management and identity data: In progress' });
  const encrypt = backupSection.getByRole('button', { name: 'Encrypt and verify the archive: Pending' });
  await expect(checkpoint).toHaveAttribute('aria-current', 'step');
  await expect(encrypt).toBeDisabled();
  await expect(backupSection.getByRole('button', { name: /^(Capture management|Encrypt and verify)/ })).toHaveText([
    /Capture management and identity data/, /Encrypt and verify the archive/,
  ]);

  revoked = true;
  await expect(backupSection.getByRole('alert')).toHaveText(/Workflow access expired/i);
  await expect(checkpoint).toHaveCount(0);
  expect(backupWrites).toBe(1);

  const restoreSection = page.locator('section').filter({ has: page.getByRole('heading', { name: 'Validate a restore' }) });
  await restoreSection.getByLabel('Archive path').fill(archive);
  await restoreSection.getByLabel('Replacement directory').fill('/private/replacement');
  const restorePassphrase = restoreSection.getByLabel('Passphrase');
  await restorePassphrase.fill(passphrase);
  await restoreSection.getByRole('checkbox', { name: 'PostgreSQL replacement' }).press('Space');
  const owner = restoreSection.getByLabel('Application owner connection URI');
  const dex = restoreSection.getByLabel('Dex owner connection URI');
  await owner.fill(ownerURI);
  await dex.fill(dexURI);
  await restoreSection.getByRole('button', { name: 'Validate restore' }).press('Enter');
  await expect(restorePassphrase).toHaveValue('');
  await expect(owner).toHaveValue('');
  await expect(dex).toHaveValue('');
  await expect(restoreSection.getByText(validationID, { exact: true })).toBeVisible();
  await expect(restoreSection.getByText('shiftbox restore activate', { exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Activate installation' })).toHaveCount(0);
  expect(restoreWrites).toBe(1);
});

// A copied session stays signed out. A fresh recovery reviewer can resolve
// a provider conflict and acknowledge retention before explicit activation.
test('withholds workspace authority and activation during recovery review', async ({ page }) => {
  let signedIn = false;
  let state = 'identity_review';
  let version = 1;
  let providerChecks = 0;
  let activations = 0;
  const reviewPath = `/api/v1/recovery/${reviewID}`;
  const review = () => ({
    id: reviewID, state, version, restoreOperationId: restoreID,
    sections: {
      identities: [{ ...administrator, primaryEmail: 'admin@example.test', membershipState: 'active', credentialMode: 'local' }],
      workspaces: providerChecks === 0 ? [] : [
        { workspaceId: 'retained-workspace', match: providerChecks === 1 ? 'ownership_conflict' : 'matched' },
      ],
    },
  });
  await page.route('**/api/v1/**', async route => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === '/api/v1/session') return route.fulfill(signedIn ? { json: {
      user: administrator, mode: 'recovery_review', installationState: 'recovery', recoveryReviewId: reviewID,
      permissions: ['recovery.review'], csrfToken: 'review-fixture',
    } } : { status: 401, json: {} });
    if (path === reviewPath && request.method() === 'GET') return route.fulfill({ json: review() });
    if (path === reviewPath && request.method() === 'PATCH') {
      expect(request.headers()['if-match']).toBe(`"${version}"`);
      expect(request.headers()['x-csrf-token']).toBe('review-fixture');
      const { section, decisions } = request.postDataJSON();
      switch (section) {
        case 'identities':
          expect(decisions).toEqual({ userIds: ['admin'] });
          state = 'provider_reconcile';
          break;
        case 'provider':
          state = ++providerChecks === 1 ? 'blocked' : 'authorization_review';
          break;
        case 'authorization_retention':
          expect(decisions).toEqual({ acceptRetention: true });
          state = 'ready';
          break;
        default:
          throw new Error('Unexpected recovery section');
      }
      version++;
      return route.fulfill({ json: review() });
    }
    if (path === `${reviewPath}/activate` && request.method() === 'POST') {
      expect(state).toBe('ready');
      expect(request.headers()['idempotency-key']).toBeTruthy();
      activations++;
      signedIn = false;
      return route.fulfill({ status: 202, json: { id: restoreID, status: 'succeeded', steps: [
        { key: 'activate', index: 0, label: 'Activate reviewed installation', status: 'succeeded' },
      ] } });
    }
    return route.fulfill({ status: 404, json: {} });
  });

  await page.goto('/recovery/');
  await expect(page.getByRole('link', { name: 'Sign in', exact: true })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Recovery review' })).toHaveCount(0);

  signedIn = true;
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Recovery review' })).toBeVisible();
  await expect(page.getByText(/Workspace entry and destructive operations stay paused/i)).toBeVisible();
  await expect(page.getByRole('link', { name: 'Workspaces' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Create backup' })).toHaveCount(0);
  const confirm = page.getByRole('button', { name: 'Confirm identities' });
  await expect(confirm).toBeDisabled();
  await page.getByRole('checkbox', { name: 'admin@example.test' }).press('Space');
  await confirm.press('Enter');
  const inventory = page.getByRole('button', { name: 'Check provider inventory' });
  await inventory.press('Enter');
  await expect(page.getByText(/retained-workspace · Ownership conflict/)).toBeVisible();
  await expect(page.getByRole('button', { name: 'Activate installation' })).toHaveCount(0);
  await inventory.press('Enter');
  await expect(page.getByText(/retained-workspace · Matched/)).toBeVisible();
  const authorization = page.getByRole('button', { name: 'Confirm authorization and retention' });
  await expect(authorization).toBeDisabled();
  await page.getByRole('checkbox', { name: /approve resuming the configured retention policy/i }).press('Space');
  await authorization.press('Enter');
  const activate = page.getByRole('button', { name: 'Activate installation' });
  await expect(activate).toBeEnabled();
  expect(activations).toBe(0);
  await activate.press('Enter');
  const completed = page.getByRole('heading', { name: 'Recovery complete' });
  await expect(completed).toBeFocused();
  await expect(page.getByRole('link', { name: 'Sign in', exact: true })).toBeVisible();
  expect(activations).toBe(1);
  await page.reload();
  await expect(page.getByRole('link', { name: 'Sign in', exact: true })).toBeVisible();
  await expect(activate).toHaveCount(0);
});
