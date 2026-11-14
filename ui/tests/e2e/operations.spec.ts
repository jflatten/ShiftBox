import { expect, test } from '@playwright/test';

const workspaceId = '42c5a1d9-fc7a-4fc7-9905-0037e6bf0130';
const operationId = '42c5a1d9-fc7a-4fc7-9905-0037e6bf0131';
const resources = { cpuCount: 2, memoryMiB: 1024, diskMiB: 4096 };
const memberSession = {
  user: { id: 'member', role: 'member' }, mode: 'normal', installationState: 'active',
  csrfToken: 'operations-fixture', permissions: [],
};

const retainedWorkspace = {
  id: workspaceId, name: 'Operations fixture', ownerId: 'member', resources,
  templateId: 'fixture-template', templateRevision: 1, platformRelease: 'fixture-release',
  desiredState: 'stopped', observedState: 'stopped', currentOperationId: null,
  lastActivityAt: null, retentionDeadline: null, version: 1,
};

test.describe('operations with a fixture controller', () => {
  // Safe-boundary waiting and rollback are server-driven. The browser never
  // sends a force request when maintenance is blocked.
  test('maintenance shows waiting and a recoverable rollback', async ({ page }) => {
    let rolledBack = false;
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
          id: operationId, status: rolledBack ? 'failed' : 'waiting', currentStep: 'update_guests', version: rolledBack ? 2 : 1,
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
    await expect(page.getByRole('button', { name: 'Update retained workspaces: In progress' })).toHaveAttribute('aria-current', 'step');
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
        lastFailedOperationId: refused ? operationId : null,
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
    await expect(page.getByText('Observed state', { exact: true }).locator('..')).toContainText('Stopped');
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

  // The preview launch uses the selected private service and leaves the
  // management origin through its scoped, referrer-free entry link.
  test('a private preview opens on its own HTTPS origin', async ({ page }) => {
    const previewOrigin = 'https://preview-4173--operations.preview.test';
    const exchange = `${previewOrigin}/_shiftbox/entry?grant=preview-fixture`;
    await page.route('**/api/v1/**', route => {
      const request = route.request();
      const { pathname } = new URL(request.url());
      if (pathname.endsWith('/session')) return route.fulfill({ json: memberSession });
      if (pathname.endsWith(`/workspaces/${workspaceId}`)) return route.fulfill({ json: {
        ...retainedWorkspace, desiredState: 'running', observedState: 'running',
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
    await page.route(url => url.href === exchange, route => {
      const request = route.request();
      expect(request.isNavigationRequest()).toBe(true);
      expect(request.method()).toBe('GET');
      expect(request.headers()['referer']).toBeUndefined();
      // Stop at the entry request. The fixture tests browser navigation and
      // referrer handling, while the real controller owns the exchange.
      return route.fulfill({ contentType: 'text/html', body: '<h1>Private preview entry fixture</h1>' });
    });
    await page.goto(`/workspaces/?id=${workspaceId}`);
    const managementOrigin = new URL(page.url()).origin;
    expect(managementOrigin).not.toBe(previewOrigin);
    await page.getByRole('button', { name: 'Open preview 4173' }).click();
    await expect(page).toHaveURL(exchange);
    await expect(page.getByRole('heading', { name: 'Private preview entry fixture' })).toBeVisible();
  });
});
