import { readFile } from 'node:fs/promises';
import { isAbsolute } from 'node:path';
import { expect, test, type Page } from '@playwright/test';

type Account = { email: string; password: string };
type Fixture = { disposable: true; managementURL: string; administrator: Account; member: Account };
type Settings = {
  enabled: boolean;
  storagePath: string;
  maxBytes: string | null;
  volumeHighPercent: number;
  volumeLowPercent: number;
};
type Snapshot = {
  version: number;
  desired: Settings;
  effective: Settings & { version: number };
  activation: { phase: string; operationId?: string };
};

const fixturePath = process.env.SHIFTBOX_REGISTRY_CACHE_FIXTURE;
const endpoint = '/api/v1/admin/image-cache';
const infrastructurePath = '/infrastructure/';
const activationTimeout = 120_000;

test.use({ trace: 'off', screenshot: 'off', video: 'off' });
test.skip(!fixturePath, 'Set SHIFTBOX_REGISTRY_CACHE_FIXTURE for a dedicated HTTPS controller and local accounts');
test.skip(({ browserName }) => browserName !== 'chromium', 'The live registry-cache journey uses Chrome');

// Change a watermark on a dedicated controller, then confirm it reaches the
// effective settings, survives browser reload, and can be restored.
test('saves and reloads registry cache settings through the live controller', async ({ browser }) => {
  test.setTimeout(5 * 60_000);
  const fixture = await readFixture();
  const context = await browser.newContext({ baseURL: fixture.managementURL });
  try {
    const page = await context.newPage();
    const csrf = await signIn(page, fixture.administrator, 'administrator');
    const original = await readSnapshot(page);
    expect(isEffective(original, original.desired), 'start with effective cache settings').toBe(true);
    const field = original.desired.volumeHighPercent < 99 ? 'volumeHighPercent' : 'volumeLowPercent';
    const value = field === 'volumeHighPercent' || original.desired.volumeLowPercent === 1
      ? original.desired[field] + 1 : original.desired[field] - 1;
    const desired = { ...original.desired, [field]: value };
    const label = field === 'volumeHighPercent' ? 'High watermark (%)' : 'Low watermark (%)';

    await page.goto(infrastructurePath);
    const cacheTab = page.getByRole('tab', { name: 'Cache' });
    await cacheTab.press('Enter');
    await expect(cacheTab).toHaveAttribute('aria-selected', 'true');
    await expect(page).toHaveURL(/\?section=registry-cache$/);
    const panel = page.getByRole('tabpanel', { name: 'Cache' });
    await expect(panel.getByRole('heading', { name: /cache usage/i })).toBeVisible();
    await expect(panel.getByRole('heading', { name: 'Cached images' })).toBeVisible();
    const input = panel.getByLabel(label, { exact: true });
    const save = panel.getByRole('button', { name: 'Save cache settings' });
    await expect(input).toHaveValue(String(original.desired[field]));
    await input.fill(String(value));
    expect((await readSnapshot(page)).desired).toEqual(original.desired);

    try {
      const put = page.waitForResponse(response => new URL(response.url()).pathname === endpoint
        && response.request().method() === 'PUT');
      await save.press('Enter');
      expect([200, 202]).toContain((await put).status());
      await expect.poll(async () => isEffective(await readSnapshot(page), desired), { timeout: activationTimeout }).toBe(true);

      await page.reload();
      await expect(cacheTab).toHaveAttribute('aria-selected', 'true');
      await expect(input).toHaveValue(String(value));
      await expect(save).toBeDisabled();
    } finally {
      const latest = await readSnapshot(page);
      if (!sameSettings(latest.desired, original.desired)) {
        await restoreSettings(page, fixture.managementURL, csrf, original.desired, desired);
      }
    }
  } finally {
    await context.close();
  }
});

// A signed-in member uses the real controller for both the deep link and direct
// API requests. A valid member CSRF header and version cannot grant authority.
test('denies the registry cache to signed-in members', async ({ browser }) => {
  const fixture = await readFixture();
  const adminContext = await browser.newContext({ baseURL: fixture.managementURL });
  const memberContext = await browser.newContext({ baseURL: fixture.managementURL });
  try {
    const admin = await adminContext.newPage();
    await signIn(admin, fixture.administrator, 'administrator');
    const snapshot = await readSnapshot(admin);

    const member = await memberContext.newPage();
    const csrf = await signIn(member, fixture.member, 'member');
    const cacheReads: string[] = [];
    member.on('request', request => {
      if (new URL(request.url()).pathname === endpoint) cacheReads.push(request.method());
    });
    await member.goto(`${infrastructurePath}?section=registry-cache`);
    await expect(member.getByText('Administrator access required.')).toBeVisible();
    await expect(member.getByRole('tab', { name: 'Cache' })).toHaveCount(0);
    expect(cacheReads).toEqual([]);
    expect((await member.request.get(endpoint)).status()).toBe(403);
    expect(await putSettings(member, fixture.managementURL, csrf, snapshot.version, snapshot.desired)).toBe(403);
  } finally {
    await Promise.all([memberContext.close(), adminContext.close()]);
  }
});

async function readFixture(): Promise<Fixture> {
  if (!fixturePath || !isAbsolute(fixturePath)) throw new Error('Supply an absolute registry cache fixture path');
  const fixture: Fixture = JSON.parse(await readFile(fixturePath, 'utf8'));
  const { administrator, member } = fixture;
  const url = new URL(fixture.managementURL);
  if (fixture.disposable !== true || url.protocol !== 'https:' || url.href !== `${url.origin}/`
    || !administrator?.email || !administrator.password || !member?.email || !member.password
    || administrator.email === member.email) {
    throw new Error('Supply a disposable HTTPS controller and distinct administrator and member accounts');
  }
  return fixture;
}

async function signIn(page: Page, account: Account, role: string): Promise<string> {
  await page.goto('/auth/login');
  await expect(page.getByRole('heading', { name: 'Log in to ShiftBox' })).toBeVisible();
  await page.getByLabel('Email', { exact: true }).fill(account.email);
  const password = page.getByLabel('Password', { exact: true });
  await expect(password).toBeEditable();
  await password.focus();
  await page.keyboard.insertText(account.password);
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await expect.poll(async () => (await page.request.get('/api/v1/session')).status()).toBe(200);
  const session = await (await page.request.get('/api/v1/session')).json();
  expect(session.user.role).toBe(role);
  expect(session.user.primaryEmail === account.email, 'the designated fixture account signed in').toBe(true);
  expect(session.mode).toBe('normal');
  expect(session.installationState).toBe('active');
  return session.csrfToken;
}

async function readSnapshot(page: Page): Promise<Snapshot> {
  const response = await page.request.get(endpoint);
  expect(response.status()).toBe(200);
  return response.json();
}

function isEffective(snapshot: Snapshot, settings: Settings) {
  return snapshot.effective.version === snapshot.version
    && snapshot.activation.phase !== 'failed'
    && sameSettings(snapshot.desired, settings)
    && sameSettings(snapshot.effective, settings);
}

function sameSettings(actual: Settings, expected: Settings) {
  return (Object.keys(expected) as (keyof Settings)[]).every(key => actual[key] === expected[key]);
}

async function putSettings(page: Page, origin: string, csrf: string, version: number, settings: Settings) {
  try {
    const response = await page.request.put(endpoint, {
      headers: { Origin: new URL(origin).origin, 'X-CSRF-Token': csrf, 'If-Match': `"${version}"` },
      data: settings,
    });
    return response.status();
  } catch {
    // Playwright request diagnostics can include the CSRF header.
    throw new Error('The registry cache settings endpoint did not respond');
  }
}

async function restoreSettings(page: Page, origin: string, csrf: string, settings: Settings, staged: Settings) {
  await expect.poll(async () => {
    const current = await readSnapshot(page);
    if (sameSettings(current.desired, settings)) return isEffective(current, settings);
    expect(current.desired, 'verify the test still owns the staged changes').toEqual(staged);
    const status = await putSettings(page, origin, csrf, current.version, settings);
    expect([200, 202, 409, 412]).toContain(status);
    return false;
  }, { timeout: activationTimeout, message: 'Restore the original registry cache settings' }).toBe(true);
}
