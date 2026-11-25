import { expect, test } from '@playwright/test';
import { expectReadableText } from './text-contrast';

// Reloading Settings must recover managed certificate state from the API and
// keep operator-managed replacement controls out of the managed workflow.
test('managed certificate renewal state is discoverable after reload', async ({ page }) => {
  await page.route('**/api/v1/**', route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/v1/session') return route.fulfill({ json: {
      user: { id: 'admin', role: 'administrator' }, mode: 'normal', installationState: 'active',
      csrfToken: 'fixture', permissions: ['infrastructure.manage', 'infrastructure.view'],
    } });
    if (path.endsWith('/settings/session')) return route.fulfill({ json: { value: 30, unit: 'minutes', version: 1 } });
    if (path.endsWith('/admin/certificate')) return route.fulfill({ json: {
      subject: 'CN=rc3.shiftbox.dev', issuer: 'CN=R13,O=Let’s Encrypt', dnsNames: ['rc3.shiftbox.dev'],
      ipAddresses: [], emailAddresses: [], uris: [], notBefore: '2026-11-25T14:00:00Z', notAfter: '2027-01-25T14:00:00Z',
      serialNumber: 'abcd', fingerprintSha256: 'a'.repeat(64), signatureAlgorithm: 'ECDSA-SHA256',
      publicKeyAlgorithm: 'ECDSA', version: 3, chainLength: 2, status: 'valid', managed: true,
      renewal: { result: 'failed', at: '2026-11-26T14:00:00Z' },
    } });
    return route.fulfill({ status: 404, json: {} });
  });
  await page.goto('/settings/');
  await page.getByRole('tab', { name: 'Certificates' }).click();
  await expect(page.getByText('Renewal failed')).toBeVisible();
  await page.reload();
  await expect(page.getByText('Renewal failed')).toBeVisible();
  await expect(page.getByText('2026-11-26 14:00:00 UTC')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Replace certificate' })).toHaveCount(0);
});

for (const width of [1280, 390]) {
  // Administrators see a separate, keyboard-operable area with the current page identified.
  test(`administration navigation at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.route('**/api/v1/**', route => {
      const pathname = new URL(route.request().url()).pathname;
      if (pathname.endsWith('/session')) return route.fulfill({ json: {
        user: { id: 'admin', role: 'administrator' }, mode: 'normal', installationState: 'active', csrfToken: 'fixture',
        permissions: ['notice.list', 'infrastructure.view', 'user.list', 'audit.list', 'backup.create'],
      } });
      if (pathname.endsWith('/audit') || pathname.endsWith('/notices')) return route.fulfill({ json: [] });
      if (pathname.endsWith('/users')) return route.fulfill({ json: [] });
      return route.fulfill({ status: 404, json: {} });
    });
    await page.goto('/audit/');
    if (width < 768) await page.getByRole('button', { name: 'Toggle navigation' }).click();
    const nav = page.getByRole('navigation', { name: 'Main navigation' });
    const group = nav.getByRole('button', { name: 'Administration' });
    await expect(group).toHaveAttribute('aria-expanded', 'true');
    for (const label of ['Notices', 'Infrastructure', 'Users', 'Audit', 'Backup & Recovery']) {
      await expect(nav.getByRole('link', { name: label })).toBeVisible();
    }
    await expect(nav.getByRole('link', { name: 'Audit' })).toHaveAttribute('aria-current', 'page');
    await group.focus();
    await page.keyboard.press('Enter');
    await expect(group).toHaveAttribute('aria-expanded', 'false');
    await page.keyboard.press('Enter');
    await expect(group).toHaveAttribute('aria-expanded', 'true');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  });
}

// Existing installation returns verify access. Revocation requires confirmation and can be resumed after reload.
test('GitHub Integration verifies and revokes repository access', async ({ page }) => {
  let state: 'installation_pending' | 'ready' | 'revoking' = 'installation_pending';
  let revocations = 0;
  await page.route('**/api/v1/**', route => {
    const pathname = new URL(route.request().url()).pathname;
    if (pathname === '/api/v1/session') return route.fulfill({ json: {
      user: { id: 'admin', role: 'administrator' }, mode: 'normal', installationState: 'active',
      csrfToken: 'fixture', permissions: ['infrastructure.manage'],
    } });
    if (pathname.endsWith('/settings/session')) return route.fulfill({ json: { value: 30, unit: 'minutes', version: 1 } });
    if (pathname.endsWith('/github-app/verify') && route.request().method() === 'POST') state = 'ready';
    if (pathname.endsWith('/github-app/revoke') && route.request().method() === 'POST') {
      expect(route.request().postDataJSON()).toEqual({ installationId: '4321' });
      state = ++revocations === 1 ? 'revoking' : 'installation_pending';
    }
    if (/\/github-app(?:\/(?:verify|revoke))?$/.test(pathname)) return route.fulfill({ json: {
      state, appName: 'ShiftBox Example', owner: 'sample-org',
      installationId: state === 'installation_pending' ? undefined : '4321',
      installUrl: state === 'installation_pending' ? 'https://github.com/apps/shiftbox-example/installations/new' : undefined,
      manageUrl: state === 'ready' ? 'https://github.com/organizations/sample-org/settings/installations/4321' : undefined,
    } });
    return route.fulfill({ status: 404, json: {} });
  });
  await page.goto('/settings/');
  await expect(page.getByRole('tab', { name: 'Sessions' })).toBeVisible();
  await expect(page.getByRole('tab', { name: 'GitHub App' })).toHaveCount(0);
  const integrations = page.getByRole('navigation', { name: 'Main navigation' }).getByRole('link', { name: 'Integrations' });
  await integrations.click();
  await expect(page).toHaveURL('/integrations/');
  await expect(integrations).toHaveAttribute('aria-current', 'page');
  const integration = page.getByRole('region', { name: 'GitHub Integration' });
  await expect(page.getByRole('tablist')).toHaveCount(0);
  await expect(integration.getByText('Repository access pending')).toBeVisible();
  await expect(integration.getByText('Select repositories', { exact: true })).toBeVisible();
  await page.goto('/settings/?section=github-app&github-install=1&installation_id=18&setup_action=install');
  const readyStatus = integration.getByRole('status').filter({ hasText: /^Ready$/ });
  await expect(readyStatus).toBeVisible();
  await expect(page).toHaveURL('/integrations/');
  await page.reload();
  await expect(readyStatus).toBeVisible();
  await expect(integration.getByText('Application Name: ShiftBox Example')).toBeVisible();
  await expect(integration.getByText('Register app', { exact: true })).toHaveCount(0);
  await expect(integration.getByText('Ready for users', { exact: true })).toHaveCount(0);

  const title = integration.getByRole('heading', { name: 'GitHub Integration' });
  const titleBox = await title.boundingBox();
  const statusBox = await readyStatus.boundingBox();
  expect(titleBox).not.toBeNull();
  expect(statusBox).not.toBeNull();
  expect(statusBox!.x).toBeGreaterThan(titleBox!.x + titleBox!.width);
  expect(statusBox!.y + statusBox!.height / 2).toBeCloseTo(titleBox!.y + titleBox!.height / 2, 0);
  const label = readyStatus.getByText('Ready', { exact: true });
  const green = await label.evaluate(element => {
    const token = getComputedStyle(element).getPropertyValue('--mantine-color-green-light-color').trim();
    const style = new Option().style;
    style.color = token;
    return style.color;
  });
  await expect(label).toHaveCSS('color', green);

  const revoke = integration.getByRole('button', { name: 'Revoke access' });
  await expectReadableText(revoke);
  await revoke.click();
  const dialog = page.getByRole('dialog', { name: 'Revoke GitHub access?' });
  const cancel = dialog.getByRole('button', { name: 'Cancel' });
  await expect(cancel).toBeFocused();
  await expectReadableText(dialog.getByRole('button', { name: 'Revoke access' }));
  await cancel.click();
  await expect(dialog).toHaveCount(0);
  expect(revocations).toBe(0);
  await integration.getByRole('button', { name: 'Revoke access' }).click();
  await dialog.getByRole('button', { name: 'Revoke access' }).click();
  await expect(integration.getByText('Revocation pending', { exact: true })).toBeVisible();
  await page.reload();
  await integration.getByRole('button', { name: 'Check revocation' }).click();
  await expect(integration.getByRole('link', { name: 'Select repositories on GitHub' })).toBeVisible();
  expect(revocations).toBe(2);
});

// Members keep their own notice history as a header utility, with no administrative navigation.
test('member navigation excludes administration', async ({ page }) => {
  await page.route('**/api/v1/**', route => {
    const pathname = new URL(route.request().url()).pathname;
    if (pathname.endsWith('/session')) return route.fulfill({ json: {
      user: { id: 'member', role: 'member' }, mode: 'normal', installationState: 'active', csrfToken: 'fixture',
      permissions: ['notice.list'],
    } });
    if (pathname.endsWith('/notices')) return route.fulfill({ json: [] });
    return route.fulfill({ status: 404, json: {} });
  });
  await page.goto('/');
  const nav = page.getByRole('navigation', { name: 'Main navigation' });
  await expect(nav.getByRole('link', { name: 'Workspaces' })).toBeVisible();
  await expect(nav.getByRole('button', { name: 'Administration' })).toHaveCount(0);
  await page.getByRole('button', { name: 'User menu' }).click();
  await expect(page.getByRole('menuitem', { name: 'Notice history' })).toHaveAttribute('href', '/notices/');
});
