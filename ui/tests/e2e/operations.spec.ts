import { expect, test } from '@playwright/test';

const workspaceId = '42c5a1d9-fc7a-4fc7-9905-0037e6bf0130';
const operationId = '42c5a1d9-fc7a-4fc7-9905-0037e6bf0131';
const resources = { cpuCount: 2, memoryMiB: 1024, diskMiB: 4096 };

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
      if (pathname.endsWith('/session')) return route.fulfill({ json: {
        user: { id: 'member', role: 'member' }, mode: 'normal', installationState: 'active',
        csrfToken: 'operations-fixture', permissions: [],
      } });
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
});
