import { lstat, readFile } from 'node:fs/promises';
import { isAbsolute } from 'node:path';
import { chromium, expect, test, type Page, type WebSocket } from '@playwright/test';

const workspaceId = '42c5a1d9-fc7a-4fc7-9905-0037e6bf0130';
const operationId = '42c5a1d9-fc7a-4fc7-9905-0037e6bf0131';
const resources = { cpuCount: 2, memoryMiB: 1024, diskMiB: 4096 };
const memberSession = {
  user: { id: 'member', role: 'member' }, mode: 'normal', installationState: 'active',
  csrfToken: 'operations-fixture', permissions: [],
};

function workspaceLifecycle(current: string, next: string | null = null, workflowOperationId: string | null = null) {
  return { current, next, mode: next === 'failed' ? 'failed' : next ? 'transitioning' : 'settled', workflowOperationId };
}

const retainedWorkspace = {
  id: workspaceId, name: 'Operations fixture', ownerId: 'member', resources,
  templateId: 'fixture-template', templateRevision: 1, platformRelease: 'fixture-release',
  desiredState: 'stopped', observedState: 'stopped', currentOperationId: null, lifecycle: workspaceLifecycle('stopped'),
  lastActivityAt: null, retentionDeadline: null, version: 1,
};

test.describe('operations with a fixture controller', () => {
  // Worker retries leave the waiting label and icon stable. A rollback still
  // appears immediately, and the browser sends no force request.
  test('maintenance shows waiting and a recoverable rollback', async ({ page }) => {
    let rolledBack = false;
    let workerClaimed = false;
    let operationReads = 0;
    const writes: string[] = [];
    await page.route('**/api/v1/**', route => {
      const { pathname } = new URL(route.request().url());
      if (route.request().method() !== 'GET') writes.push(pathname);
      if (pathname.endsWith('/session')) return route.fulfill({ json: {
        user: { id: 'admin', role: 'administrator' }, mode: 'normal', installationState: 'active',
        csrfToken: 'operations-fixture', permissions: ['infrastructure.view', 'maintenance.manage'],
      } });
      if (pathname.endsWith('/infrastructure')) return route.fulfill({ json: {
        provider: { name: 'UTM', version: 'fixture', architecture: 'amd64' }, capacity: resources, version: 1, conditions: [],
      } });
      if (pathname.endsWith('/workspaces')) return route.fulfill({ json: { items: [] } });
      if (pathname.endsWith('/maintenance')) return route.fulfill({ json: [{
        id: operationId, operationId, releaseId: 'fixture-release', deadline: '2099-01-01T12:00:00Z',
        state: rolledBack ? 'blocked' : 'active',
      }] });
      if (pathname.endsWith('/events')) return route.fulfill({ contentType: 'text/event-stream', body: 'retry: 1000\n\n' });
      if (pathname.endsWith(`/operations/${operationId}`)) {
        operationReads++;
        return route.fulfill({ json: {
          id: operationId, status: rolledBack ? 'failed' : workerClaimed ? 'running' : 'waiting', currentStep: 'update_guests', version: operationReads,
          steps: [{ key: 'update_guests', index: 0, label: 'Update retained workspaces',
            status: rolledBack ? 'failed' : 'active',
            detail: rolledBack ? 'Review the failed workspace updates before advancing the controller release.'
              : 'Waiting for safe workspace updates to finish.' }],
        } });
      }
      return route.fulfill({ status: 404, json: {} });
    });
    await page.goto('/infrastructure/');
    await page.getByRole('button', { name: 'View progress' }).click();
    await expect(page.getByText('Waiting for safe workspace updates to finish.')).toBeVisible();
    const waiting = page.getByText('Waiting — Update retained workspaces', { exact: true });
    const activeStep = page.getByRole('button', { name: 'Update retained workspaces: In progress' });
    await expect(waiting).toBeVisible();
    await expect(activeStep).toHaveAttribute('aria-current', 'step');
    const announcements = await waiting.evaluateHandle(element => {
      const changes: string[] = [];
      const observer = new MutationObserver(() => changes.push(element.textContent ?? ''));
      observer.observe(element, { subtree: true, childList: true, characterData: true });
      return { changes, observer };
    });
    for (const claimed of [true, false, true]) {
      const refreshed = page.waitForResponse(response => response.url().endsWith(`/operations/${operationId}`));
      workerClaimed = claimed;
      expect((await (await refreshed).json()).status).toBe(claimed ? 'running' : 'waiting');
    }
    await expect(waiting).toBeVisible();
    await expect(activeStep.locator('.mantine-Stepper-stepLoader')).toHaveCount(0);
    expect(await announcements.evaluate(({ changes, observer }) => { observer.disconnect(); return changes; })).toEqual([]);
    await announcements.dispose();
    await expect.poll(() => operationReads).toBeGreaterThanOrEqual(2);
    rolledBack = true;
    await expect(page.getByRole('button', { name: 'Update retained workspaces: Failed' })).toBeVisible();
    await expect(page.getByText('Review the failed workspace updates before advancing the controller release.')).toBeVisible();
    await expect(page.getByText(/schedule a replacement campaign/i)).toBeVisible();
    await expect(page.getByRole('button', { name: /force/i })).toHaveCount(0);
    expect(writes).toEqual([]);
  });

  // The destructive dialog requires both acknowledgements before sending an
  // explicit deletion request and shows unresolved cleanup as waiting.
  test('deletion explains data loss and shows unresolved cleanup', async ({ page }) => {
    const workspaceName = 'Operations fixture';
    let deleting = false;
    await page.route('**/api/v1/**', route => {
      const { pathname } = new URL(route.request().url());
      if (pathname.endsWith('/session')) return route.fulfill({ json: memberSession });
      if (pathname.endsWith(`/workspaces/${workspaceId}`)) return route.fulfill({ json: {
        id: workspaceId, name: workspaceName, resources, desiredState: deleting ? 'deleted' : 'stopped', observedState: 'stopped',
        currentOperationId: deleting ? operationId : null, version: deleting ? 2 : 1,
        lifecycle: deleting ? workspaceLifecycle('stopped', 'deleted', operationId) : workspaceLifecycle('stopped'),
      } });
      if (pathname.endsWith('/actions/delete')) {
        expect(route.request().method()).toBe('POST');
        expect(route.request().postDataJSON()).toEqual({ confirmDataLoss: true });
        expect(route.request().headers()['if-match']).toBe('"1"');
        deleting = true;
        return route.fulfill({ status: 202, json: { id: operationId } });
      }
      if (pathname.endsWith('/events')) return route.fulfill({ contentType: 'text/event-stream', body: 'retry: 1000\n\n' });
      if (pathname.endsWith(`/operations/${operationId}`)) return route.fulfill({ json: {
        id: operationId, status: 'waiting', currentStep: 'delete', version: 1,
        steps: [{ key: 'delete', index: 0, label: 'Delete workspace and retained files', status: 'active',
          detail: 'Waiting for the provider to verify workspace cleanup. Unconfirmed resources remain reserved.' }],
      } });
      return route.fulfill({ status: 404, json: {} });
    });
    await page.goto(`/workspaces/?id=${workspaceId}`);
    await page.getByRole('tab', { name: 'Settings', exact: true }).click();
    await page.getByRole('button', { name: 'Delete workspace' }).click();
    const dialog = page.getByRole('dialog', { name: 'Delete workspace' });
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await expect(dialog.getByText('Management backups exclude workspace VM disks.')).toBeVisible();
    await expect(dialog.getByText(/including project files, the developer home, and named application volumes/i)).toBeVisible();
    const confirm = dialog.getByRole('button', { name: 'Delete permanently' });
    await dialog.getByRole('textbox', { name: 'Type the workspace name' }).fill(workspaceName);
    await expect(confirm).toBeDisabled();
    expect(deleting).toBe(false);
    await dialog.getByRole('checkbox', { name: /pushed work/i }).check();
    await confirm.click();
    await expect(dialog).toBeHidden();
    await expect(page.getByText(/unconfirmed resources remain reserved/i)).toBeVisible();
    expect(deleting).toBe(true);
  });

  // The workspace renders the server's inactivity clock and keeps cleanup
  // paused during controller recovery grace.
  test('retention shows recorded activity and recovery grace', async ({ page }) => {
    const lastActivity = '2026-11-14T12:00:00Z';
    const deadline = '2026-11-15T12:00:00Z';
    const graceUntil = '2026-11-15T13:00:00Z';
    let state: 'disabled' | 'grace' | 'ready' = 'disabled';
    await page.route('**/api/v1/**', route => {
      const { pathname } = new URL(route.request().url());
      if (pathname.endsWith('/session')) return route.fulfill({ json: memberSession });
      if (pathname.endsWith(`/workspaces/${workspaceId}`)) return route.fulfill({ json: {
        ...retainedWorkspace,
        lastActivityAt: lastActivity,
        retentionDeadline: state === 'disabled' ? null : deadline,
        retention: { state, ...(state === 'grace' ? { graceUntil } : {}) },
      } });
      return route.fulfill({ status: 404, json: {} });
    });
    await page.goto(`/workspaces/?id=${workspaceId}`);
    await expect(page.locator(`time[datetime="${lastActivity}"]`)).toBeVisible();
    await expect(page.getByText('No deadline scheduled')).toBeVisible();
    await expect(page.getByText('Automatic retention cleanup')).toHaveCount(0);
    state = 'grace';
    await page.reload();
    await expect(page.getByText('Retention cleanup paused')).toBeVisible();
    await expect(page.getByText(/Recovery grace ends/).locator('time')).toHaveAttribute('datetime', graceUntil);
    await expect(page.locator(`time[datetime="${deadline}"]`)).toBeVisible();
    await expect(page.getByText('Workspace deletion is in progress')).toHaveCount(0);
    state = 'ready';
    await page.reload();
    await expect(page.getByText('Automatic retention cleanup')).toBeVisible();
    await expect(page.getByText('Retention cleanup paused')).toHaveCount(0);
    await expect(page.getByText(/Management backups exclude workspace disks/)).toBeVisible();
  });

  // A stopped workspace with a refused recovery still has its retained
  // storage and offers no IDE entry while the controller reports failure.
  test('a refused long-stop recovery leaves the workspace stopped', async ({ page }) => {
    const refusal = 'The retained guest identity is ineligible for automatic recovery. Ask an administrator to verify the workspace.';
    let startRequested = false;
    let refused = false;
    await page.route('**/api/v1/**', route => {
      const request = route.request();
      const { pathname } = new URL(request.url());
      if (pathname.endsWith('/session')) return route.fulfill({ json: memberSession });
      if (pathname.endsWith(`/workspaces/${workspaceId}`)) return route.fulfill({ json: {
        ...retainedWorkspace,
        currentOperationId: startRequested && !refused ? operationId : null,
        lifecycle: startRequested ? workspaceLifecycle('stopped', refused ? 'failed' : 'vm-running', operationId) : workspaceLifecycle('stopped'),
        desiredState: startRequested ? 'running' : 'stopped',
        canOpenIde: false,
      } });
      if (pathname.endsWith('/actions/start')) {
        expect(request.method()).toBe('POST');
        expect(request.headers()['if-match']).toBe('"1"');
        startRequested = true;
        return route.fulfill({ status: 202, json: { id: operationId } });
      }
      if (pathname.endsWith('/events')) return route.fulfill({ contentType: 'text/event-stream', body: 'retry: 1000\n\n' });
      if (pathname.endsWith(`/operations/${operationId}`)) return route.fulfill({ json: {
        id: operationId, status: refused ? 'failed' : 'waiting', currentStep: 'start', version: refused ? 2 : 1,
        steps: [{ key: 'start', index: 0, label: 'Start workspace', status: refused ? 'failed' : 'active',
          detail: refused ? refusal : 'Checking the retained guest identity.' }],
      } });
      return route.fulfill({ status: 404, json: {} });
    });
    await page.goto(`/workspaces/?id=${workspaceId}`);
    await page.getByRole('button', { name: 'Start workspace', exact: true }).click();
    await expect(page.getByText('Checking the retained guest identity.')).toBeVisible();
    refused = true;
    await expect(page.getByText(refusal)).toBeVisible();
    await expect(page.getByRole('button', { name: 'Start workspace: Failed' })).toBeVisible();
    await expect(page.getByText('Current state', { exact: true }).locator('..')).toContainText('Stopped');
    await expect(page.getByText(/4096 MiB disk/)).toBeVisible();
    await expect(page.getByRole('button', { name: 'Open IDE' })).toHaveCount(0);
  });

  // A controller revocation on the active event stream immediately removes
  // the previously visible operation evidence.
  test('live operation revocation removes cached progress', async ({ page }) => {
    let revoked = false;
    const detail = 'Waiting for this workspace to reach a safe boundary.';
    await page.route('**/api/v1/**', route => {
      const { pathname } = new URL(route.request().url());
      if (pathname.endsWith('/session')) return route.fulfill({ json: memberSession });
      if (pathname.endsWith(`/workspaces/${workspaceId}`)) return route.fulfill({ json: {
        ...retainedWorkspace, currentOperationId: operationId,
      } });
      if (pathname.endsWith('/events')) return route.fulfill({ contentType: 'text/event-stream', body: revoked
        ? 'event: authorization.revoked\ndata: {}\n\n' : 'retry: 1000\n\n' });
      if (pathname.endsWith(`/operations/${operationId}`)) return route.fulfill({ json: {
        id: operationId, status: 'waiting', currentStep: 'update_guests', version: 1,
        steps: [{ key: 'update_guests', index: 0, label: 'Update workspace', status: 'active', detail }],
      } });
      return route.fulfill({ status: 404, json: {} });
    });
    await page.goto(`/workspaces/?id=${workspaceId}`);
    await expect(page.getByText(detail)).toBeVisible();
    revoked = true;
    await expect(page.getByText('You no longer have access to this operation.')).toBeVisible();
    await expect(page.getByText(detail)).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Update workspace: In progress' })).toHaveCount(0);
  });

  // The preview launch opens the scoped, referrer-free entry in a separate tab.
  test('a private preview opens in a new tab on its own HTTPS origin', async ({ page, context }) => {
    const previewOrigin = 'https://preview-4173--operations.preview.test';
    const exchange = `${previewOrigin}/_shiftbox/entry?grant=preview-fixture`;
    await page.route('**/api/v1/**', route => {
      const request = route.request();
      const { pathname } = new URL(request.url());
      if (pathname.endsWith('/session')) return route.fulfill({ json: memberSession });
      if (pathname.endsWith(`/workspaces/${workspaceId}`)) return route.fulfill({ json: {
        ...retainedWorkspace, desiredState: 'running', observedState: 'running', lifecycle: workspaceLifecycle('ready'),
        canOpenPreviews: true, previewPorts: [4173],
      } });
      if (pathname.endsWith('/entry-grants')) {
        expect(request.method()).toBe('POST');
        expect(request.headers()['x-csrf-token']).toBe(memberSession.csrfToken);
        expect(request.postDataJSON()).toEqual({ service: 'preview-4173' });
        return route.fulfill({ json: { exchangeUrl: exchange } });
      }
      return route.fulfill({ status: 404, json: {} });
    });
    await context.route(url => url.href === exchange, route => {
      const request = route.request();
      expect(request.isNavigationRequest()).toBe(true);
      expect(request.method()).toBe('GET');
      expect(request.headers()['referer']).toBeUndefined();
      // Stop at the entry request. The fixture tests browser navigation and
      // referrer handling, while the real controller owns the exchange.
      return route.fulfill({ contentType: 'text/html', body: '<h1>Private preview entry fixture</h1>' });
    });
    await page.goto(`/workspaces/?id=${workspaceId}`);
    await page.getByRole('tab', { name: 'Connections', exact: true }).click();
    const consoleURL = page.url();
    expect(new URL(consoleURL).origin).not.toBe(previewOrigin);
    const [previewTab] = await Promise.all([
      context.waitForEvent('page'),
      page.getByRole('button', { name: 'Open preview 4173' }).click(),
    ]);
    await expect(previewTab).toHaveURL(exchange);
    await expect(previewTab.getByRole('heading', { name: 'Private preview entry fixture' })).toBeVisible();
    expect(await previewTab.evaluate(() => window.opener)).toBeNull();
    await expect(page).toHaveURL(consoleURL);
  });
});

type OperationsAccount = { email: string; password: string };
type OperationsFixture = {
  disposable: true;
  managementURL: string;
  previewURL: string;
  workspaceID: string;
  owner: OperationsAccount;
  administrator: OperationsAccount;
};

const liveFixturePath = process.env.SHIFTBOX_OPERATIONS_FIXTURE;
if (liveFixturePath) test.use({ trace: 'off', screenshot: 'off', video: 'off' });

test.describe('operations with a live controller', () => {
  test.skip(!liveFixturePath, 'Set SHIFTBOX_OPERATIONS_FIXTURE for the disposable UTM journey');
  test.skip(({ browserName }) => browserName !== 'chromium', 'The live operations journey uses Chrome');

  // The runner supplies a running member-owned workspace. Its configured
  // HTTP preview opens a same-origin WebSocket that sends an initial frame.
  // Seed an exact connection grant for the separate administrator account.
  test('private preview isolates origins and closes a revoked administrator tunnel', async () => {
    test.setTimeout(90_000);
    const fixture = await readOperationsFixture(liveFixturePath);
    const management = new URL(fixture.managementURL).origin;
    const preview = new URL(fixture.previewURL);
    const service = preview.hostname.split('--')[0];
    const workspacePath = `/workspaces/?id=${fixture.workspaceID}`;
    const entryURL = new URL(`/api/v1/workspaces/${fixture.workspaceID}/entry-grants`, management).href;
    const browser = await chromium.launch({ channel: 'chrome' });
    try {
      const ownerContext = await browser.newContext();
      const adminContext = await browser.newContext();
      const owner = await ownerContext.newPage();
      const administrator = await adminContext.newPage();
      await Promise.all([
        signInForOperations(owner, management, fixture.owner, 'member'),
        signInForOperations(administrator, management, fixture.administrator, 'administrator'),
      ]);
      const adminSessionResponse = await administrator.request.get(new URL('/api/v1/session', management).href);
      expect(adminSessionResponse.status()).toBe(200);
      const { csrfToken } = await adminSessionResponse.json() as { csrfToken: string };
      expect(Boolean(csrfToken), 'the administrator has a current CSRF token').toBe(true);

      await test.step('Require an authorized entry from the management origin', async () => {
        const direct = await owner.request.get(preview.href, { maxRedirects: 0 });
        expect(direct.status()).toBe(403);
        const wrongOrigin = await requestLivePreviewEntry(administrator, entryURL, preview.origin, csrfToken, service);
        expect(wrongOrigin.status()).toBe(403);
        expect((await wrongOrigin.json()).code).toBe('auth.invalid_origin_or_csrf');
      });

      const [ownerPreview, adminPreview] = await test.step('Open both private previews through the browser', async () => {
        const pages = await Promise.all([
          openLivePreview(owner, management, preview, workspacePath, service),
          openLivePreview(administrator, management, preview, workspacePath, service),
        ]);
        const managementCookieNames = (await adminContext.cookies(management)).map(cookie => cookie.name);
        const previewCookieNames = (await adminContext.cookies(preview.href)).map(cookie => cookie.name);
        expect(managementCookieNames).toContain('__Host-shiftbox_session');
        expect(managementCookieNames).not.toContain('__Host-shiftbox_origin');
        expect(previewCookieNames).toContain('__Host-shiftbox_origin');
        expect(previewCookieNames).not.toContain('__Host-shiftbox_session');
        return pages;
      });
      const { socket: ownerSocket } = ownerPreview;
      const { socket: adminSocket } = adminPreview;

      await test.step('Revoke only the administrator grant and observe the open tunnel close', async () => {
        await administrator.goto(new URL(workspacePath, management).href);
        await administrator.getByRole('button', { name: `Revoke access for ${fixture.administrator.email}`, exact: true }).click();
        const dialog = administrator.getByRole('dialog', { name: 'Revoke connection grant' });
        await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
        expect(adminSocket.isClosed(), 'administrator tunnel is open before revocation').toBe(false);
        expect(ownerSocket.isClosed(), 'owner tunnel is open before revocation').toBe(false);
        await dialog.getByRole('button', { name: 'Revoke access', exact: true }).click();
        await expect(dialog).toBeHidden();
        await expect.poll(() => adminSocket.isClosed(), { timeout: 10_000 }).toBe(true);
        expect(ownerSocket.isClosed(), 'revocation preserves the owner tunnel').toBe(false);
        expect((await adminPreview.tab.reload({ waitUntil: 'domcontentloaded' }))?.status()).toBe(403);
        const nextEntry = await requestLivePreviewEntry(administrator, entryURL, management, csrfToken, service);
        expect(nextEntry.status()).toBe(403);
        expect((await owner.request.get(preview.href, { maxRedirects: 0 })).status()).toBe(200);
      });
    } finally {
      await browser.close();
    }
  });
});

async function readOperationsFixture(path: string | undefined): Promise<OperationsFixture> {
  if (!path || !isAbsolute(path)) throw new Error('SHIFTBOX_OPERATIONS_FIXTURE must be an absolute file path');
  const info = await lstat(path);
  if (!info.isFile() || (info.mode & 0o077) !== 0) throw new Error('Use a private regular operations fixture file');
  let fixture: OperationsFixture;
  let management: URL;
  let preview: URL;
  try {
    fixture = JSON.parse(await readFile(path, 'utf8'));
    management = new URL(fixture.managementURL);
    preview = new URL(fixture.previewURL);
  } catch {
    throw new Error('Invalid operations fixture JSON or URL');
  }
  const bareHTTPS = (url: URL) => url.protocol === 'https:' && !url.username && !url.password &&
    url.pathname === '/' && !url.search && !url.hash;
  const previewName = /^preview-([1-9][0-9]{0,4})--([^.]+)\./.exec(preview.hostname);
  if (fixture.disposable !== true || !bareHTTPS(management) || !bareHTTPS(preview) ||
      management.hostname === preview.hostname || management.port !== preview.port ||
      !previewName || Number(previewName[1]) > 65535 || previewName[2] !== fixture.workspaceID ||
      !/^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/.test(fixture.workspaceID) ||
      !fixture.owner?.email || !fixture.owner.password || !fixture.administrator?.email ||
      !fixture.administrator.password || fixture.owner.email === fixture.administrator.email) {
    throw new Error('Supply a disposable HTTPS installation, separate preview origin, workspace UUID, and two fixture accounts');
  }
  return fixture;
}

async function requestLivePreviewEntry(page: Page, entryURL: string, origin: string, csrfToken: string, service: string) {
  try {
    return await page.request.post(entryURL, {
      headers: { Origin: origin, 'X-CSRF-Token': csrfToken }, data: { service }, maxRedirects: 0,
    });
  } catch {
    // Playwright request errors can contain the CSRF header. Keep it out of test output.
    throw new Error('The preview entry endpoint did not respond');
  }
}

async function signInForOperations(page: Page, management: string, account: OperationsAccount, role: string) {
  await page.goto(new URL('/auth/login', management).href);
  await expect(page.getByRole('heading', { name: 'Log in to ShiftBox' })).toBeVisible();
  await page.getByLabel('Email', { exact: true }).fill(account.email);
  const password = page.getByLabel('Password', { exact: true });
  await expect(password).toBeEditable();
  await password.focus();
  await page.keyboard.insertText(account.password);
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await page.waitForURL(url => !url.pathname.startsWith('/dex/'));
  const sessionURL = new URL('/api/v1/session', management).href;
  await expect.poll(async () => (await page.request.get(sessionURL)).status()).toBe(200);
  const session = await (await page.request.get(sessionURL)).json();
  expect(session.user.role).toBe(role);
  expect(session.user.primaryEmail === account.email, 'the designated fixture account signed in').toBe(true);
  expect(session.mode).toBe('normal');
  expect(session.installationState).toBe('active');
}

async function openLivePreview(page: Page, management: string, preview: URL, workspacePath: string, service: string) {
  const previewSocketOrigin = `wss://${preview.host}`;
  let liveSocket: WebSocket | undefined;
  const consoleURL = new URL(workspacePath, management).href;
  await page.goto(consoleURL);
  await page.getByRole('tab', { name: 'Connections', exact: true }).click();
  const [tab] = await Promise.all([
    page.context().waitForEvent('page').then(tab => {
      tab.on('websocket', socket => {
        if (new URL(socket.url()).origin === previewSocketOrigin) {
          socket.once('framereceived', () => { liveSocket = socket; });
        }
      });
      return tab;
    }),
    page.getByRole('button', { name: `Open preview ${service.slice('preview-'.length)}`, exact: true }).click(),
  ]);
  await tab.waitForURL(preview.href);
  await expect(page).toHaveURL(consoleURL);
  await expect.poll(() => liveSocket !== undefined && !liveSocket.isClosed(), {
    timeout: 20_000, message: 'the preview tunnel must deliver a frame before revocation',
  }).toBe(true);
  if (!liveSocket) throw new Error('Preview socket unavailable');
  return { tab, socket: liveSocket };
}
