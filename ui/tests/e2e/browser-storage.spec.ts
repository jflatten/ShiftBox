import { randomUUID } from 'node:crypto';
import { expect, test, type Locator, type Page } from '@playwright/test';

const fixtureURL = process.env.SHIFTBOX_STORAGE_UI_URL;
const userId = '8ad9e975-d4c5-4ea9-b671-a5a62c300118';
const secretName = 'STORAGE_PROBE';

test.use({ channel: 'chrome', trace: 'off', screenshot: 'off', video: 'off' });
test.skip(!fixtureURL, 'Set SHIFTBOX_STORAGE_UI_URL to a separate loopback static export');
test.skip(({ browserName }) => browserName !== 'chromium', 'Browser storage fixture uses Chrome');

function staticOrigin() {
  const url = new URL(fixtureURL ?? '');
  if (!['http:', 'https:'].includes(url.protocol) || url.hostname !== '127.0.0.1' || !url.port ||
      url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
    throw new Error('Use an explicit loopback origin for the disposable static export');
  }
  return url.origin;
}

async function enterPrivateValue(page: Page, field: Locator, value: string) {
  await field.focus();
  await page.keyboard.insertText(value);
}

async function expectNoBrowserRetention(page: Page, canaries: string[]) {
  const found = await page.evaluate(async values => {
    const stored: string[] = [location.href, document.referrer, window.name, document.cookie, document.body.innerText];
    for (const storage of [localStorage, sessionStorage]) {
      for (let index = 0; index < storage.length; index++) {
        const key = storage.key(index);
        if (key !== null) stored.push(key, storage.getItem(key) ?? '');
      }
    }
    stored.push(...Array.from(document.querySelectorAll('input, textarea'), field =>
      (field as HTMLInputElement | HTMLTextAreaElement).value));
    const databases = await indexedDB.databases();
    const cachesOpened = await globalThis.caches.keys();
    return {
      retained: stored.some(item => values.some(value => item.includes(value))),
      databases: databases.length,
      caches: cachesOpened.length,
    };
  }, canaries);
  expect(found).toEqual({ retained: false, databases: 0, caches: 0 });
}

// A protected value belongs in the write request body only. A response that
// unexpectedly echoes it must leave no visible or persisted browser copy.
test('keeps application secrets out of browser storage and URLs', async ({ page }) => {
  const origin = staticOrigin();
  const saved = randomUUID(), rejected = randomUUID(), unexpected = randomUUID(), csrf = randomUUID();
  const canaries = [saved, rejected, unexpected, csrf];
  let savedOnce = false;
  let correctWrites = 0;
  const observedURLs: string[] = [];
  const consoleMessages: string[] = [];
  page.on('request', request => observedURLs.push(request.url()));
  page.on('console', message => consoleMessages.push(message.text()));
  await page.route('**/*', route => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.origin !== origin) return route.abort();
    if (!url.pathname.startsWith('/api/v1/')) return route.continue();
    if (url.pathname === '/api/v1/session') return route.fulfill({ json: {
      user: { id: userId, role: 'member' }, mode: 'normal', installationState: 'active',
      permissions: ['secret.manage_user'], csrfToken: csrf,
    } });
    if (url.pathname === '/api/v1/secrets' && request.method() === 'GET') return route.fulfill({ json: savedOnce ? [{
      name: secretName, explicitEmpty: false, updatedAt: '2026-11-14T00:00:00Z', value: unexpected,
    }] : [] });
    if (url.pathname === `/api/v1/secrets/user/${userId}/${secretName}` && request.method() === 'PUT') {
      if (request.postDataJSON()?.value === (savedOnce ? rejected : saved) &&
          request.headers()['x-csrf-token'] === csrf && url.search === '') {
        correctWrites++;
      }
      if (!savedOnce) {
        savedOnce = true;
        return route.fulfill({ status: 204 });
      }
      return route.fulfill({ status: 400, contentType: 'application/problem+json', body: JSON.stringify({
        type: 'about:blank', title: 'Echoed fixture value', status: 400, code: 'request.invalid',
        requestId: 'fixture', detail: rejected,
      }) });
    }
    return route.fulfill({ status: 404, json: {} });
  });

  await page.goto(origin + '/account/');
  await page.getByRole('button', { name: 'Add secret' }).click();
  const create = page.getByRole('dialog', { name: 'Add secret' });
  await create.getByLabel('Secret name').fill(secretName);
  const value = create.getByLabel('Secret value');
  await expect(value).toHaveAttribute('type', 'password');
  await expect(value).toHaveAttribute('autocomplete', 'off');
  await enterPrivateValue(page, value, saved);
  await create.getByRole('button', { name: 'Save secret' }).click();
  await expect(page.getByRole('status', { name: '' }).filter({ hasText: 'Secret saved.' })).toBeVisible();
  await expect(page.getByRole('rowheader', { name: secretName })).toBeVisible();
  await expectNoBrowserRetention(page, canaries);

  await page.getByRole('button', { name: 'Replace ' + secretName }).click();
  const replace = page.getByRole('dialog', { name: 'Replace secret' });
  const replacement = replace.getByLabel('Secret value');
  await expect(replacement).toHaveValue('');
  await enterPrivateValue(page, replacement, rejected);
  await replace.getByRole('button', { name: 'Save secret' }).click();
  await expect(replace.getByText('Unable to complete the request')).toBeVisible();
  expect((await replace.innerText()).includes(rejected), 'error display omits private input').toBe(false);
  await replace.getByRole('button', { name: 'Cancel' }).click();
  await expectNoBrowserRetention(page, canaries);

  await page.reload();
  await expect(page.getByRole('rowheader', { name: secretName })).toBeVisible();
  await expectNoBrowserRetention(page, canaries);
  expect(correctWrites).toBe(2);
  expect(observedURLs.some(url => canaries.some(value => url.includes(value))), 'request URLs omit private values').toBe(false);
  expect(consoleMessages.some(message => canaries.some(value => message.includes(value))), 'browser logs omit private values').toBe(false);
});
