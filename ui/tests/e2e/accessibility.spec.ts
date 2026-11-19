import { expect, test, type Locator, type Page, type Route } from '@playwright/test';

const operationID = '42c5a1d9-fc7a-4fc7-9905-0037e6bf0119';
const workspaceID = '42c5a1d9-fc7a-4fc7-9905-0037e6bf0120';
const templateID = '42c5a1d9-fc7a-4fc7-9905-0037e6bf0121';
const reviewID = '42c5a1d9-fc7a-4fc7-9905-0037e6bf0122';
const target = { id: 'target', primaryEmail: 'target@example.test', membershipState: 'active', role: 'member', credentialMode: 'local', version: 1 };
const resources = { cpuCount: 2, memoryMiB: 1024, diskMiB: 4096 };
const operation = (labels: [string, string, string]) => ({
  id: operationID, status: 'running', currentStep: 'current', version: 1,
  steps: [
    { key: 'next', index: 2, label: labels[2], status: 'pending' },
    { key: 'done', index: 0, label: labels[0], status: 'succeeded', detail: 'Earlier step evidence.' },
    { key: 'current', index: 1, label: labels[1], status: 'active', detail: 'Current step evidence.' },
  ],
});

type Fixture = (route: Route, path: string) => Promise<void> | undefined;
const admin = (permissions: string[]) => ({
  user: { id: 'admin', role: 'administrator' }, mode: 'normal', installationState: 'active',
  csrfToken: 'accessibility-test', permissions,
});

async function fixture(page: Page, respond: Fixture, workflow?: ReturnType<typeof operation>) {
  await page.route('**/api/v1/**', route => {
    const path = new URL(route.request().url()).pathname;
    if (workflow && path === `/api/v1/operations/${operationID}`) return route.fulfill({ json: workflow });
    if (path.endsWith('/events')) return route.fulfill({ contentType: 'text/event-stream', body: 'retry: 3000\n\n' });
    return respond(route, path) ?? route.fulfill({ status: 404, json: {} });
  });
}

// macOS WebKit uses Option+Tab for full page navigation with the default
// keyboard settings. Exercise the browser shortcut without changing settings.
async function tabForward(page: Page) {
  const browser = page.context().browser()?.browserType().name();
  await page.keyboard.press(browser === 'webkit' && process.platform === 'darwin' ? 'Alt+Tab' : 'Tab');
}

async function tabTo(page: Page, control: Locator) {
  await expect(control).toBeVisible();
  for (let i = 0; i < 40; i++) {
    if (await control.evaluate(element => element === document.activeElement)) return;
    await tabForward(page);
  }
  await expect(control).toBeFocused();
}

async function activate(page: Page, control: Locator, key = 'Enter') {
  await tabTo(page, control);
  await page.keyboard.press(key);
}

async function enter(page: Page, field: Locator, value: string) {
  await tabTo(page, field);
  await page.keyboard.insertText(value);
}

async function choosePEM(page: Page, button: Locator, name: string) {
  const chooser = page.waitForEvent('filechooser');
  await activate(page, button);
  await (await chooser).setFiles({ name, mimeType: 'text/plain', buffer: Buffer.from('Fixture only') });
}

async function checkWorkflow(page: Page, labels: [string, string, string], scope: Page | Locator = page) {
  const complete = scope.getByRole('button', { name: `${labels[0]}: Completed` });
  const current = scope.getByRole('button', { name: `${labels[1]}: In progress` });
  const pending = scope.getByRole('button', { name: `${labels[2]}: Pending` });
  const announcement = scope.getByRole('status').filter({ hasText: labels[1] });
  await expect(current).toHaveAttribute('aria-current', 'step');
  await expect(announcement).toHaveAttribute('aria-live', 'polite');
  await expect(pending).toBeDisabled();
  await tabTo(page, complete);
  await page.keyboard.press('Enter');
  await expect(complete).toBeFocused();
  await expect(scope.getByText('Earlier step evidence.')).toBeVisible();
  await expect(scope.getByText('Current step evidence.')).toBeVisible();
  await expect(current).toHaveAttribute('aria-current', 'step');
  await tabForward(page);
  await expect(current).toBeFocused();
  await tabForward(page);
  await expect(pending).not.toBeFocused();
}

function workspaceLifecycle(current: string, next: string | null = null, workflowOperationId: string | null = null) {
  return { current, next, mode: next === 'failed' ? 'failed' : next ? 'transitioning' : 'settled', workflowOperationId };
}

test.use({ viewport: { width: 390, height: 844 }, trace: 'off' });

// Both chips and indeterminate progress share one cell and settle automatically after a poll.
test('workspace inventory animates the transition inside the state cell', async ({ page }) => {
  let transitioning = true;
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: admin([]) });
    if (path === '/api/v1/workspaces') return route.fulfill({ json: { items: [{
      id: workspaceID, name: 'Transition fixture', resources, desiredState: 'running',
      observedState: transitioning ? 'starting' : 'running', currentOperationId: transitioning ? operationID : null,
      lifecycle: transitioning ? workspaceLifecycle('stopped', 'vm-running', operationID) : workspaceLifecycle('ready'),
    }], nextCursor: null } });
  });
  await page.goto('/workspaces/');
  const row = page.getByRole('row', { name: /Transition fixture/ });
  const state = row.getByRole('cell').nth(1);
  const connector = state.getByRole('progressbar', { name: 'State transition in progress' });
  await expect(connector).toBeVisible();
  await expect(page.getByRole('columnheader', { name: 'Target State' })).toHaveCount(0);
  await expect(state.getByText('VM-running')).toBeVisible();
  await expect(row.getByRole('cell').last()).toHaveText('Actions');
  const observedBounds = (await state.getByText('Stopped').locator('..').boundingBox())!;
  const desiredBounds = (await state.getByText('VM-running').locator('..').boundingBox())!;
  const connectorBounds = (await connector.boundingBox())!;
  expect(connectorBounds.x).toBeGreaterThan(observedBounds.x + observedBounds.width);
  expect(connectorBounds.x + connectorBounds.width).toBeLessThan(desiredBounds.x);
  expect(connectorBounds.y + connectorBounds.height / 2).toBeCloseTo(observedBounds.y + observedBounds.height / 2, 0);
  transitioning = false;
  await expect(connector).toHaveCount(0, { timeout: 8000 });
  await expect(state.getByText('Ready')).toBeVisible();
  await expect(state.getByText('Stopped')).toHaveCount(0);
});

// Both layouts halve the connector and spacer width while keeping a named progress bar.
test('workspace lifecycle connector is half width in inventory and detail', async ({ page }) => {
  const current = { id: workspaceID, name: 'Compact transition', resources, desiredState: 'running', observedState: 'stopped',
    lifecycle: workspaceLifecycle('stopped', 'vm-running'), currentOperationId: null };
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: { ...admin([]), user: { id: 'member', role: 'member' } } });
    if (path.endsWith(`/workspaces/${workspaceID}`)) return route.fulfill({ json: current });
    if (path === '/api/v1/workspaces') return route.fulfill({ json: { items: [current], nextCursor: null } });
  });
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 844 });
    for (const compact of [true, false]) {
      await page.goto(compact ? '/workspaces/' : `/workspaces/?id=${workspaceID}`);
      const connector = page.getByRole('progressbar', { name: 'State transition in progress' });
      await expect(connector).toBeVisible();
      await expect(connector).not.toHaveAttribute('aria-valuenow');
      const spacing = await connector.evaluate((element, inInventory) => {
        const grid = element.parentElement!;
        const [left, gapBefore, slot, gapAfter, right] = getComputedStyle(grid).gridTemplateColumns.split(' ').map(parseFloat);
        const previousSlot = inInventory ? 80 : Math.max(16, (grid.getBoundingClientRect().width - left - right - 20) / 2);
        return { actual: gapBefore + slot + gapAfter, half: (previousSlot + 20) / 2 };
      }, compact);
      expect(spacing.actual).toBeCloseTo(spacing.half, 0);
    }
  }
});

// Controller recovery preserves the same connector during the outage and restores keyboard focus.
test('workspace inventory freezes its connector during a controller outage', async ({ page }) => {
  let connectivity = 'transitioning';
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: { ...admin([]), user: { id: 'member', role: 'member' } } });
    if (path === '/api/v1/workspaces') {
      if (connectivity === 'offline') return route.abort('internetdisconnected');
      return route.fulfill({ json: { items: [{ id: workspaceID, name: 'Outage fixture', resources,
        desiredState: 'running', observedState: connectivity === 'ready' ? 'running' : 'stopped',
        lifecycle: connectivity === 'ready' ? workspaceLifecycle('ready') : workspaceLifecycle('stopped', 'vm-running'),
      }], nextCursor: null } });
    }
  });
  await page.goto('/workspaces/');
  const connector = page.getByRole('progressbar', { name: 'State transition in progress' });
  await expect(connector).toBeVisible();
  const refresh = page.getByRole('button', { name: 'Refresh' });
  await refresh.focus();
  connectivity = 'offline';
  await page.keyboard.press('Enter');
  const outage = page.getByRole('dialog', { name: 'Controller unavailable' });
  await expect(outage).toHaveCount(1);
  await expect(connector.locator('[data-paused]')).toHaveCSS('animation-play-state', 'paused');
  await expect(page.getByText('Trying to reconnect to ShiftBox…')).toBeFocused();

  connectivity = 'ready';
  await expect(outage).toHaveCount(0, { timeout: 8000 });
  await expect(page.getByRole('row', { name: /Outage fixture/ }).getByText('Ready')).toBeVisible();
  await expect(connector).toHaveCount(0);
  await expect(refresh).toBeFocused();
});

// Filtering cannot strand the user on a stale cursor, deleted
// workspaces stay hidden by default, and status chips fit a narrow screen.
test('workspace inventory filters and detail layout', async ({ page }) => {
  const current = { id: workspaceID, name: 'Keyboard workspace', resources, desiredState: 'stopped', observedState: 'stopped',
    currentOperationId: null, lifecycle: workspaceLifecycle('stopped'), templateRevision: 1, platformRelease: 'Demo release', version: 1, canEditPreviewPorts: true };
  const deleted = { ...current, id: 'deleted-workspace', name: 'Deleted example', observedState: 'deleted', desiredState: 'deleted', lifecycle: workspaceLifecycle('deleted') };
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: { ...admin([]), user: { id: 'member', role: 'member' } } });
    if (path.endsWith(`/workspaces/${workspaceID}`)) return route.fulfill({ json: current });
    if (path === '/api/v1/workspaces') {
      const query = new URL(route.request().url()).searchParams;
      const items = query.get('lifecycleState') === 'deleted' ? [deleted] :
        query.get('cursor') ? [{ ...current, id: 'second-workspace', name: 'Second workspace' }] :
        query.get('excludeDeleted') === 'true' ? [current] : [current, deleted];
      return route.fulfill({ json: { items, nextCursor: query.has('cursor') || query.has('lifecycleState') ? null : 'next-page' } });
    }
  });
  await page.goto('/workspaces/');
  await expect(page.getByRole('link', { name: 'Keyboard workspace', exact: true })).toBeVisible();
  const workspaceRow = page.getByRole('row', { name: /Keyboard workspace/ });
  const stopped = workspaceRow.locator('label.mantine-Chip-label').filter({ hasText: 'Stopped' });
  await expect(stopped).toHaveCount(1);
  await expect(stopped).toHaveCSS('background-color', 'rgb(111, 111, 111)');
  await expect(stopped).toHaveCSS('color', 'rgb(255, 255, 255)');
  await expect(workspaceRow.getByRole('checkbox')).toHaveCount(0);
  await expect(workspaceRow.getByText('Desired:')).toHaveCount(0);
  const actions = workspaceRow.getByRole('button', { name: 'Actions for Keyboard workspace' });
  await activate(page, actions);
  await expect(actions).toHaveAttribute('aria-expanded', 'true');
  const menu = page.getByRole('menu');
  const start = menu.getByRole('menuitem', { name: 'Start', exact: true });
  await expect(start).toBeInViewport();
  await expect(menu.getByRole('menuitem', { name: 'Open workspace' })).toHaveAttribute('href', '/workspaces/?id=' + workspaceID);
  await page.keyboard.press('ArrowDown');
  await expect(start).toBeFocused();
  await page.keyboard.press('End');
  const deleteAction = menu.getByRole('menuitem', { name: 'Delete workspace' });
  await expect(deleteAction).toBeFocused();
  await page.keyboard.press('Enter');
  const deletion = page.getByRole('dialog', { name: 'Delete workspace' });
  await expect(deletion.getByRole('button', { name: 'Cancel' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(deletion).toBeHidden();
  await expect(actions).toBeFocused();
  await expect(menu).toBeHidden();
  await expect(page).toHaveURL(/\/workspaces\/$/);
  await expect(page.getByRole('link', { name: 'Deleted example', exact: true })).toHaveCount(0);
  await activate(page, page.getByRole('button', { name: 'Next page' }));
  await expect(page.getByText('Page 2', { exact: true })).toBeVisible();
  const history = page.getByRole('checkbox', { name: 'Show deleted workspaces' });
  await activate(page, history, 'Space');
  await expect(page.getByRole('link', { name: 'Deleted example', exact: true })).toBeVisible();
  await expect(page.getByText('Page 1', { exact: true })).toBeVisible();
  const state = page.getByLabel('Current state', { exact: true });
  await tabTo(page, state);
  await state.selectOption('deleted');
  await expect(state).toHaveValue('deleted');
  await expect(page.getByRole('link', { name: 'Keyboard workspace', exact: true })).toHaveCount(0);
  await activate(page, history, 'Space');
  await expect(state).toHaveValue('');
  await expect(page.getByRole('link', { name: 'Deleted example', exact: true })).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await activate(page, actions);
  await menu.getByRole('menuitem', { name: 'Open workspace' }).click();
  await expect(page).toHaveURL(new RegExp('\\?id=' + workspaceID + '$'));
  await expect(page.getByRole('heading', { name: 'Keyboard workspace', level: 1 })).toBeVisible();
  await tabTo(page, page.getByRole('button', { name: 'Start workspace', exact: true }));
  await expect(page.getByRole('region', { name: 'Previews', exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

// Setup validation focuses the invalid field and the keyboard can reach the
// one-time password acknowledgement without visiting informational steps.
test('setup keyboard path and current stage', async ({ page }) => {
  await fixture(page, (route, path) => {
    if (path.endsWith('/status')) return route.fulfill({ json: { state: 'bootstrap', completedSteps: [], availableSteps: ['administrator'] } });
    if (path.endsWith('/session')) return route.fulfill({ status: 401, json: {} });
    if (path.endsWith('/administrator')) return route.fulfill({ status: 201, json: { temporaryPassword: 'disposable-test-output' } });
  });
  await page.goto('/setup/');
  await expect(page.getByRole('button', { name: 'Administrator', exact: true })).toHaveAttribute('aria-current', 'step');
  const email = page.getByRole('textbox', { name: 'Administrator email' });
  await tabForward(page);
  await expect(email).toBeFocused();
  const create = page.getByRole('button', { name: 'Create administrator' });
  await activate(page, create);
  await expect(email).toBeFocused();
  await page.keyboard.insertText('administrator@example.test');
  await activate(page, create);
  const continueButton = page.getByRole('button', { name: 'Continue to sign-in' });
  await expect(continueButton).toBeDisabled();
  await activate(page, page.getByRole('checkbox', { name: 'I saved the temporary password' }), 'Space');
  await activate(page, continueButton);
  await expect(page.getByRole('button', { name: 'Password', exact: true })).toHaveAttribute('aria-current', 'step');
  await tabTo(page, page.getByRole('link', { name: 'Sign in', exact: true }));
});

// A restricted setup session progresses across password, database, certificate,
// provider, and activation using the keyboard and an intercepted controller.
test('restricted setup keyboard progression', async ({ page }) => {
  const stages = ['password', 'data_profile', 'certificate', 'provider', 'activation'];
  let stage = 0;
  let artifactReady = false;
  await fixture(page, (route, path) => {
    if (path.endsWith('/status')) return route.fulfill({ json: {
      state: 'configuring', completedSteps: ['administrator', ...stages.slice(0, stage)], availableSteps: [stages[stage]],
      compatibleArtifact: artifactReady, capacityVersion: 1, managementOrigin: 'https://shiftbox.example.test',
    } });
    if (path.endsWith('/session')) return route.fulfill({ json: { mode: 'bootstrap_restricted', csrfToken: 'fixture' } });
    if (path.endsWith('/password') || path.endsWith('/data-profile') || path.endsWith('/certificate') || path.endsWith('/capacity')) {
      stage++;
      return route.fulfill({ json: {} });
    }
    if (path.endsWith('/platform')) {
      artifactReady = true;
      return route.fulfill({ json: {} });
    }
    if (path.endsWith('/activate')) return route.fulfill({ json: { status: 'succeeded' } });
  });
  await page.goto('/setup/');
  const current = (name: string) => expect(page.getByRole('button', { name, exact: true })).toHaveAttribute('aria-current', 'step');
  await current('Password');
  await enter(page, page.getByLabel(/^Current password/i), 'fixture-old-password');
  await enter(page, page.getByLabel(/^New password/i), 'fixture-new-password');
  await enter(page, page.getByLabel(/^Confirm password/i), 'fixture-new-password');
  await activate(page, page.getByRole('button', { name: 'Change password' }));
  await expect(page.getByText('Sign in again with your new password.')).toBeVisible();
  await tabTo(page, page.getByRole('link', { name: 'Sign in', exact: true }));
  await page.reload();
  await current('Database');
  await activate(page, page.getByRole('button', { name: 'Validate database' }));
  await current('Certificate');
  for (const [label, value] of [
    ['Management address', 'https://shiftbox.example.test'],
    ['IDE domain suffix', 'ide.example.test'],
    ['Preview domain suffix', 'preview.example.test'],
  ]) {
    await enter(page, page.getByRole('textbox', { name: label }), value);
  }
  await choosePEM(page, page.getByRole('button', { name: 'Certificate chain' }), 'fixture.crt');
  await choosePEM(page, page.getByRole('button', { name: 'Private key' }), 'fixture.pem');
  await activate(page, page.getByRole('button', { name: 'Validate certificate' }));
  await current('Provider');
  await activate(page, page.getByRole('button', { name: 'Validate platform image' }));
  await expect(page.getByRole('button', { name: 'Validate platform image' })).toBeHidden();
  for (const [label, value] of [['vCPU limit', '2'], ['Memory limit (MiB)', '1024'], ['Disk limit (MiB)', '4096']]) {
    await enter(page, page.getByRole('textbox', { name: label, exact: true }), value);
  }
  await activate(page, page.getByRole('button', { name: 'Save capacity' }));
  await current('Activation');
  const finish = page.getByRole('button', { name: 'Activate installation' });
  await expect(finish).toBeDisabled();
  await activate(page, page.getByRole('checkbox', { name: /need to sign in again/ }), 'Space');
  await activate(page, finish);
  await tabTo(page, page.getByRole('link', { name: 'Sign in to ShiftBox' }));
});

for (const configuration of [
  { name: 'GitHub connection', page: '/account/', permission: 'git.manage', endpoint: '/api/v1/git/authorization',
    button: 'Reconnect GitHub', link: 'Continue to GitHub', url: 'https://github.com/login/oauth/authorize?state=fixture', later: 'Authorize on GitHub' },
  { name: 'external identity renewal', page: '/account/', permission: 'session.external_reauthorize', endpoint: '/api/v1/session/external-reauthorization',
    button: 'Renew authorization', link: 'Continue to organization', url: '/dex/auth?state=fixture', later: 'Authorize with your organization' },
]) {
  // The keyboard can open the handoff and focus moves to the authorized link.
  test(`${configuration.name} keyboard handoff`, async ({ page }) => {
    await fixture(page, (route, path) => {
      if (path.endsWith('/session')) return route.fulfill({ json: {
        ...admin([configuration.permission]), externalAuthorizationDueAt: '2099-01-01T00:00:00Z',
      } });
      if (path !== configuration.endpoint) return;
      if (route.request().method() === 'GET') return route.fulfill({ json: { state: 'reconnect_required' } });
      const url = new URL(configuration.url, route.request().url());
      return route.fulfill({ json: { authorizationUrl: url.href, expiresAt: '2099-01-01T00:00:00Z' } });
    });
    await page.goto(configuration.page);
    const future = page.getByRole('button', { name: configuration.later });
    await expect(future).toBeVisible();
    await activate(page, page.getByRole('button', { name: configuration.button }));
    await expect(page.getByRole('link', { name: configuration.link })).toBeFocused();
    await expect(future).not.toBeFocused();
  });
}

// The launch picker reflows at narrow widths, keeps metadata legible in dark mode, and shares its name filter between views.
test('workspace launch picker reflows and shares table and card search', async ({ page }) => {
  const projects = Array.from({ length: 12 }, (_, index) => ({
    id: `project-${index + 1}`, name: `Project ${String(index + 1).padStart(2, '0')}`, currentRevision: 1,
    repositoryUrl: 'https://github.com/example/project', resourceProfileName: 'Small', providerKind: 'ec2',
  }));
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: admin([]) });
    if (path.endsWith('/workspaces')) return route.fulfill({ json: { items: [], nextCursor: null } });
    if (path.endsWith('/templates')) return route.fulfill({ json: { items: projects, nextCursor: null } });
  });
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto('/workspaces/');
  await activate(page, page.getByRole('link', { name: 'Launch Project' }));
  await expect(page).toHaveURL(/\/templates\/$/);
  await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Main navigation' }).getByRole('link', { name: 'Projects' })).toHaveAttribute('aria-current', 'page');
  const first = page.getByRole('button', { name: 'Launch workspace for Project 01' });
  const second = page.getByRole('button', { name: 'Launch workspace for Project 02' });
  await expect(second).toBeVisible();
  expect((await first.boundingBox())?.y).toBe((await second.boundingBox())?.y);
  const contrast = await first.getByText('Revision 1').evaluate(element => {
    function luminance(color: string) {
      const components = color.match(/\d+/g)?.slice(0, 3).map(component => Number(component) / 255) ?? [];
      return components.reduce((sum, channel, index) => {
        const linear = channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
        return sum + linear * [0.2126, 0.7152, 0.0722][index];
      }, 0);
    }
    const text = luminance(getComputedStyle(element).color);
    const card = luminance(getComputedStyle(element.closest('article')!).backgroundColor);
    return (Math.max(text, card) + 0.05) / (Math.min(text, card) + 0.05);
  });
  expect(contrast).toBeGreaterThanOrEqual(4.5);

  await page.setViewportSize({ width: 390, height: 844 });
  expect((await second.boundingBox())?.y).toBeGreaterThan((await first.boundingBox())?.y ?? 0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(390);
  await page.getByText('Table', { exact: true }).click();
  const table = page.getByRole('table', { name: 'Projects' });
  await expect(table.getByRole('row')).toHaveCount(11);
  await page.getByRole('button', { name: 'Next page' }).click();
  await expect(table.getByRole('row')).toHaveCount(3);
  await page.getByRole('searchbox', { name: 'Search projects' }).fill('project 02');
  await expect(table.getByRole('row')).toHaveCount(2);
  await expect(table.getByRole('rowheader', { name: /Project 02/ })).toBeVisible();
  await page.getByText('Cards', { exact: true }).click();
  await expect(first).toHaveCount(0);
  await activate(page, second);
  await expect(page.getByRole('textbox', { name: 'Workspace name' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(second).toBeFocused();
});

for (const failed of [false, true]) {
  // Keyboard launch keeps the Stepper visible and puts a failed step's
  // server-provided explanation in an inline warning.
  test(failed ? 'workspace launch failure warning' : 'workspace launch progress', async ({ page }) => {
    const labels: [string, string, string] = ['Provision workspace', 'Enroll guest', 'Verify IDE'];
    const progress = operation(labels);
    const snapshot = failed ? { ...progress, status: 'failed', steps: progress.steps.map(step =>
      step.status === 'active' ? { ...step, status: 'failed', detail: 'The guest could not enroll.' } : step,
    ) } : progress;
    await fixture(page, (route, path) => {
      if (path.endsWith('/session')) return route.fulfill({ json: admin([]) });
      if (path.endsWith('/templates')) return route.fulfill({ json: { items: [{ id: templateID, name: 'Keyboard project', currentRevision: 1,
        repositoryUrl: 'https://github.com/example/project', platformArtifactId: 'Ubuntu', resourceProfileId: 'Small' }], nextCursor: null } });
      if (path.endsWith('/workspaces') && route.request().method() === 'POST') return route.fulfill({ status: 202, json: { id: operationID, workspaceId: workspaceID } });
    }, snapshot);
    await page.goto('/templates/');
    await activate(page, page.getByRole('button', { name: 'Launch workspace for Keyboard project' }), 'Space');
    const review = page.getByRole('dialog', { name: 'Review launch' });
    await expect(review).toBeVisible();
    await tabTo(page, page.getByRole('textbox', { name: 'Workspace name' }));
    await page.keyboard.insertText('Keyboard workspace');
    await activate(page, review.getByRole('button', { name: 'Launch workspace', exact: true }));
    await expect(page.getByRole('heading', { name: 'Launch workspace', exact: true })).toBeFocused();
    if (failed) {
      const warning = page.getByRole('alert', { name: 'Workspace launch failed' });
      await expect(warning).toContainText('The guest could not enroll.');
      await expect(page.getByRole('button', { name: 'Provision workspace: Completed' })).toBeVisible();
      await expect(page.getByRole('button', { name: 'Enroll guest: Failed' })).toBeVisible();
      await expect(page.getByRole('button', { name: 'Verify IDE: Pending' })).toBeDisabled();
    } else {
      await checkWorkflow(page, labels);
    }
  });
}

// A direct visit to an existing workspace surfaces the saved launch failure
// above its Stepper, including the next step that could not run.
test('workspace detail launch failure warning', async ({ page }) => {
  const progress = operation(['Provision workspace', 'Configure container', 'Verify IDE']);
  const failed = { ...progress, type: 'provision', status: 'failed', steps: progress.steps.map(step =>
    step.status === 'active' ? { ...step, status: 'failed', detail: 'The guest could not prepare the repository.' } : step,
  ) };
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: admin([]) });
    if (path.endsWith(`/workspaces/${workspaceID}`)) return route.fulfill({ json: {
      id: workspaceID, name: 'Failed workspace', observedState: 'running', desiredState: 'running', resources,
      currentOperationId: null, lifecycle: workspaceLifecycle('vm-running', 'failed', operationID),
    } });
  }, failed);
  await page.goto(`/workspaces/?id=${workspaceID}`);
  const warning = page.getByRole('alert', { name: 'Workspace launch failed' });
  await expect(warning).toContainText('The guest could not prepare the repository.');
  await expect(page.getByRole('button', { name: 'Configure container: Failed' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Verify IDE: Pending' })).toBeDisabled();
});

for (const [action, button, label] of [
  ['start', 'Start workspace', 'Start guest'], ['stop', 'Stop workspace', 'Stop workload'],
  ['rebuild', 'Rebuild container', 'Rebuild workspace'], ['repair', 'Start repair', 'Open repair environment'],
  ['retry', 'Exit repair and retry setup', 'Resume workspace setup'], ['update', 'Update now', 'Activate the guest update safely'],
]) {
  // Each submitted workspace action moves keyboard focus to its progress
  // when the action button becomes unavailable.
  test(`workspace ${action} progress`, async ({ page }) => {
    const labels: [string, string, string] = ['Check workspace', label, 'Verify result'];
    let started = false;
    const state = action === 'start' || action === 'update' ? 'stopped' : 'running';
    await fixture(page, (route, path) => {
      if (path.endsWith('/session')) return route.fulfill({ json: { ...admin([]), user: { id: 'member', role: 'member' } } });
      if (path.endsWith(`/workspaces/${workspaceID}`)) return route.fulfill({ json: {
        id: workspaceID, name: 'Keyboard workspace', resources, desiredState: state, observedState: state, version: 1,
        lifecycle: workspaceLifecycle(state === 'stopped' ? 'stopped' : 'ready', null, started ? operationID : null),
        currentOperationId: started ? operationID : null, canStartRepair: action === 'repair', canRetryAfterRepair: action === 'retry',
        maintenance: action === 'update' ? { releaseId: 'update-release', deadline: '2026-11-15T14:30:00Z',
          state: started ? 'waiting_safe' : 'pending', operationId: started ? operationID : null } : undefined,
      } });
      if (path.endsWith(`/actions/${action}`)) {
        started = true;
        return route.fulfill({ status: 202, json: { id: operationID } });
      }
    }, operation(labels));
    await page.goto(`/workspaces/?id=${workspaceID}`);
    await activate(page, page.getByRole('button', { name: button, exact: true }));
    await expect(page.getByRole('heading', { name: 'Workspace operation' })).toBeFocused();
    await checkWorkflow(page, labels);
  });
}

// Delete confirmation traps focus, returns it on escape, and gates the
// operation until the keyboard user supplies both confirmations.
test('workspace deletion focus and progress', async ({ page }) => {
  const labels: [string, string, string] = ['Revoke access', 'Stop workspace', 'Remove retained files'];
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: { ...admin([]), user: { id: 'member', role: 'member' } } });
    if (path.endsWith(`/workspaces/${workspaceID}`)) return route.fulfill({ json: {
      id: workspaceID, name: 'Keyboard workspace', resources, desiredState: 'stopped', observedState: 'stopped', lifecycle: workspaceLifecycle('stopped'), version: 1,
    } });
    if (path.endsWith('/actions/delete')) return route.fulfill({ status: 202, json: { id: operationID } });
  }, operation(labels));
  await page.goto(`/workspaces/?id=${workspaceID}`);
  const trigger = page.getByRole('button', { name: 'Delete workspace' });
  await activate(page, trigger);
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(trigger).toBeFocused();
  await page.keyboard.press('Enter');
  const confirm = dialog.getByRole('button', { name: 'Delete permanently' });
  await expect(confirm).toBeDisabled();
  await tabTo(page, dialog.getByRole('textbox', { name: /type the workspace name/i }));
  await page.keyboard.insertText('Keyboard workspace');
  await activate(page, dialog.getByRole('checkbox', { name: /pushed work/i }), 'Space');
  await activate(page, confirm);
  await expect(dialog).toBeHidden();
  await checkWorkflow(page, labels);
});

// Existing maintenance progress is reachable by keyboard. Certificate file
// buttons open native pickers, and missing input focuses the first picker.
test('maintenance and certificate progress', async ({ page }) => {
  const maintenanceLabels: [string, string, string] = ['Schedule update', 'Wait for safe boundary', 'Verify update'];
  const certificateLabels: [string, string, string] = ['Validate certificate', 'Install certificate', 'Verify certificate'];
  await fixture(page, (route, path) => {
    if (path === '/api/v1/session') return route.fulfill({ json: admin(['infrastructure.view', 'infrastructure.manage', 'maintenance.manage']) });
    if (path.endsWith('/settings/session')) return route.fulfill({ json: { value: 30, unit: 'minutes', version: 1 } });
    if (path.endsWith('/workspaces')) return route.fulfill({ json: { items: [] } });
    if (path.endsWith('/infrastructure')) return route.fulfill({ json: {
      provider: { name: 'UTM', version: 'fixture', architecture: 'amd64' }, capacity: resources, version: 1, conditions: [],
    } });
    if (path.endsWith('/maintenance')) return route.fulfill({ json: [{ id: 'campaign', operationId: operationID,
      releaseId: 'fixture-release', deadline: '2099-01-01T00:00:00Z', state: 'active' }] });
    if (path.endsWith('/certificate')) return route.fulfill({ json: route.request().method() === 'PUT' ? operation(certificateLabels) : {
      subject: 'CN=management.test', issuer: 'CN=Fixture CA', dnsNames: ['management.test'], ipAddresses: [], emailAddresses: [], uris: [],
      notBefore: '2026-11-01T12:00:00Z', notAfter: '2027-11-01T12:00:00Z', serialNumber: '1234', fingerprintSha256: 'ab'.repeat(32),
      signatureAlgorithm: 'ECDSA-SHA256', publicKeyAlgorithm: 'ECDSA', version: 3, chainLength: 2, status: 'valid',
    } });
  }, operation(maintenanceLabels));
  await page.goto('/settings/');
  await tabTo(page, page.getByRole('tab', { name: 'Sessions' }));
  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('tab', { name: 'Certificates' })).toHaveAttribute('aria-selected', 'true');
  const installed = page.getByRole('region', { name: 'Installed certificate' });
  await expect(installed).toHaveClass(/mantine-Card-root/);
  await expect(installed.getByText('CN=Fixture CA')).toBeVisible();
  const replace = page.getByRole('button', { name: 'Replace certificate' });
  await activate(page, replace);
  const chain = page.getByRole('button', { name: 'Certificate chain' });
  const key = page.getByRole('button', { name: 'Private key' });
  await expect(chain).toBeFocused();
  for (const [control, name] of [[chain, 'fixture.crt'], [key, 'fixture.pem']] as const) {
    await choosePEM(page, control, name);
  }
  await activate(page, replace);
  await checkWorkflow(page, certificateLabels);
  await page.goto('/infrastructure/');
  await tabTo(page, page.getByRole('tab', { name: 'Platform' }));
  await page.keyboard.press('ArrowRight');
  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('tab', { name: 'Maintenance' })).toHaveAttribute('aria-selected', 'true');
  await activate(page, page.getByRole('button', { name: 'View progress' }));
  const maintenance = page.locator('section').filter({ has: page.getByRole('heading', { name: 'Maintenance', exact: true }) });
  await checkWorkflow(page, maintenanceLabels, maintenance);
});

// Keyboard tab navigation must preserve draft settings and global warnings,
// including when the narrow viewport collapses the main navigation.
test('Infrastructure categories preserve drafts and warnings on a narrow screen', async ({ page }) => {
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: {
      ...admin(['infrastructure.view', 'infrastructure.manage', 'maintenance.manage', 'secret.manage_organization']),
      organizationSecretScopeId: 'organization-fixture',
    } });
    if (path.endsWith('/infrastructure')) return route.fulfill({ json: {
      provider: { name: 'utm', version: 'fixture', architecture: 'amd64', networkModes: ['shared'] },
      capacity: resources, version: 1,
      conditions: [{ key: 'browser_certificate.expiring', severity: 'warning', active: true, detail: 'Replace the certificate soon.' }],
    } });
    if (path.endsWith('/workspaces')) return route.fulfill({ json: { items: [] } });
    if (path.endsWith('/maintenance') || path.endsWith('/secrets')) return route.fulfill({ json: [] });
  });
  await page.goto('/infrastructure/');
  const save = page.getByRole('button', { name: 'Save capacity budget' });
  await expect(save).toBeDisabled();
  const cpu = page.getByRole('textbox', { name: 'CPU limit' });
  await tabTo(page, cpu);
  await page.keyboard.press('ControlOrMeta+A');
  await page.keyboard.insertText('6');
  await expect(save).toBeEnabled();
  for (const [current, next] of [['Platform', 'Cache'], ['Cache', 'Maintenance'], ['Maintenance', 'Platform']]) {
    await tabTo(page, page.getByRole('tab', { name: current }));
    await page.keyboard.press('ArrowRight');
    await expect(page.getByRole('tab', { name: next })).toHaveAttribute('aria-selected', 'true');
    await expect(page.getByRole('region', { name: 'Persistent conditions' }).getByText('Replace the certificate soon.')).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    if (next === 'Maintenance') await enter(page, page.getByRole('textbox', { name: 'Target release ID' }), 'draft-release');
  }
  await expect(cpu).toHaveValue('6');
  await activate(page, page.getByRole('button', { name: 'Discard changes' }));
  await expect(cpu).toHaveValue('2');
  await expect(save).toBeDisabled();
  await tabTo(page, page.getByRole('tab', { name: 'Platform' }));
  await page.keyboard.press('ArrowRight');
  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('textbox', { name: 'Target release ID' })).toHaveValue('draft-release');
});

// Saving a timeout through the two controls preserves both values on reload.
test('Administration Settings saves and reloads a session timeout', async ({ page }) => {
  let timeout = { value: 30, unit: 'minutes', version: 1 };
  await fixture(page, (route, path) => {
    if (path === '/api/v1/session') return route.fulfill({ json: {
      ...admin(['infrastructure.view', 'infrastructure.manage', 'secret.manage_organization']), organizationSecretScopeId: 'organization-fixture',
    } });
    if (path.endsWith('/infrastructure')) return route.fulfill({ json: { conditions: [] } });
    if (path.endsWith('/secrets')) return route.fulfill({ json: [] });
    if (path.endsWith('/settings/session') && route.request().method() === 'PUT') {
      expect(route.request().headers()['if-match']).toBe('"1"');
      expect(route.request().postDataJSON()).toEqual({ value: 2, unit: 'days' });
      timeout = { value: 2, unit: 'days', version: 2 };
      return route.fulfill({ status: 204 });
    }
    if (path.endsWith('/settings/session')) return route.fulfill({ json: timeout });
  });
  await page.goto('/settings/');
  await activate(page, page.getByRole('button', { name: 'Toggle navigation' }));
  await expect(page.getByRole('link', { name: 'Settings' })).toHaveAttribute('aria-current', 'page');
  await activate(page, page.getByRole('button', { name: 'Toggle navigation' }));
  const value = page.getByRole('textbox', { name: 'Session timeout' });
  const unit = page.getByRole('combobox', { name: 'Time unit' });
  await expect(value).toHaveValue('30');
  await expect(unit).toHaveValue('minutes');
  await value.fill('2');
  await unit.selectOption({ label: 'Days' });
  await activate(page, page.getByRole('button', { name: 'Save session timeout' }));
  await expect(page.getByText('Session timeout saved.')).toBeVisible();
  await page.reload();
  await expect(value).toHaveValue('2');
  await expect(unit).toHaveValue('days');
  await activate(page, page.getByRole('tab', { name: 'Sessions' }));
  await page.keyboard.press('ArrowRight');
  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('heading', { name: 'Organization secrets' })).toBeVisible();
  await page.reload();
  await expect(page.getByRole('tab', { name: 'Secrets' })).toHaveAttribute('aria-selected', 'true');
});

// Clearing individual budgets must show Unlimited after saving and reloading,
// while a finite memory limit remains configured.
test('blank capacity limits persist as Unlimited', async ({ page }) => {
  let capacity = { ...resources };
  let version = 1;
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: admin(['infrastructure.view', 'infrastructure.manage']) });
    if (path.endsWith('/workspaces')) return route.fulfill({ json: { items: [] } });
    if (path.endsWith('/resource-profiles')) return route.fulfill({ json: [] });
    if (path.endsWith('/infrastructure')) return route.fulfill({ json: {
      provider: { name: 'utm', version: 'fixture', architecture: 'amd64' }, capacity, version, conditions: [],
    } });
    if (path.endsWith('/capacity') && route.request().method() === 'PUT') {
      expect(route.request().headers()['if-match']).toBe('"1"');
      capacity = route.request().postDataJSON();
      expect(capacity).toEqual({ cpuCount: 0, memoryMiB: 1024, diskMiB: 0 });
      version++;
      return route.fulfill({ status: 204 });
    }
  });
  await page.goto('/infrastructure/');
  await expect(page.getByRole('tab', { name: 'Platform' })).toHaveAttribute('aria-selected', 'true');
  await page.getByRole('textbox', { name: 'CPU limit' }).fill('');
  await page.getByRole('textbox', { name: 'Disk limit (MiB)' }).fill('');
  await page.getByRole('button', { name: 'Save capacity budget' }).click();
  await expect(page.getByText('Capacity budget saved.', { exact: true })).toBeVisible();
  await page.reload();
  for (const name of ['CPU limit', 'Disk limit (MiB)']) {
    const field = page.getByRole('textbox', { name });
    await expect(field).toHaveValue('');
    await expect(field).toHaveAttribute('placeholder', 'Unlimited');
    await expect(field).not.toHaveAttribute('required');
  }
  await expect(page.getByRole('textbox', { name: 'Memory limit (MiB)' })).toHaveValue('1024');
  await expect(page.getByRole('button', { name: 'Save capacity budget' })).toBeDisabled();
});

// People dialogs start on Cancel. The keyboard reaches the stop operation
// and can acknowledge the one-time password before closing the reset dialog.
test('offboarding and administrator password reset', async ({ page }) => {
  const labels: [string, string, string] = ['Close sessions', 'Request workspace stop', 'Verify workspace stopped'];
  let disabled = false;
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: admin(['user.list', 'user.disable', 'user.reset_password']) });
    if (path.endsWith('/users')) return route.fulfill({ json: [{ ...target, membershipState: disabled ? 'disabled' : 'active' }] });
    if (path.endsWith('/workspaces')) return route.fulfill({ json: { items: [{ id: workspaceID, name: 'Keyboard workspace', observedState: 'running' }], nextCursor: null } });
    if (path.endsWith('/password-reset')) return route.fulfill({ json: { user: { ...target, version: 2 }, temporaryPassword: 'disposable-reset-output' } });
    if (path.endsWith('/users/target')) {
      disabled = true;
      return route.fulfill({ json: { ...target, membershipState: 'disabled', stopOperations: [{ workspaceId: workspaceID, operationId: operationID }] } });
    }
  }, operation(labels));
  await page.goto('/users/');
  await activate(page, page.getByRole('button', { name: 'Actions for target@example.test' }));
  await expect(page.getByRole('menu').locator('[data-autofocus]')).toBeFocused();
  await page.keyboard.press('ArrowDown');
  await expect(page.getByRole('menuitem', { name: 'Reset password' })).toBeFocused();
  await page.keyboard.press('Enter');
  const reset = page.getByRole('dialog', { name: 'Reset password' });
  await expect(reset.getByRole('button', { name: 'Cancel' })).toBeFocused();
  await activate(page, reset.getByRole('button', { name: 'Generate temporary password' }));
  await expect(reset.getByText('disposable-reset-output')).toBeVisible();
  const done = reset.getByRole('button', { name: 'Done' });
  await expect(done).toBeDisabled();
  await activate(page, reset.getByRole('checkbox', { name: 'I saved the temporary password' }), 'Space');
  await activate(page, done);
  await expect(reset).toBeHidden();
  await activate(page, page.getByRole('button', { name: 'Actions for target@example.test' }));
  await expect(page.getByRole('menu').locator('[data-autofocus]')).toBeFocused();
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('ArrowDown');
  await expect(page.getByRole('menuitem', { name: 'Disable account' })).toBeFocused();
  await page.keyboard.press('Enter');
  const disable = page.getByRole('dialog');
  await expect(disable.getByRole('button', { name: 'Cancel' })).toBeFocused();
  await activate(page, disable.getByRole('button', { name: 'Confirm disable' }));
  await expect(disable.getByRole('status').filter({ hasText: 'Sign-in sessions and tracked connections closed.' })).toBeFocused();
  await checkWorkflow(page, labels, disable);
});

// Escape restores focus to the People action that opened the dialog so the
// keyboard user can continue from the same account row.
test('People dialogs restore focus on Escape', async ({ page }) => {
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: admin(['user.list', 'user.disable', 'user.reset_password']) });
    if (path.endsWith('/users')) return route.fulfill({ json: [target] });
    if (path.endsWith('/workspaces')) return route.fulfill({ json: { items: [], nextCursor: null } });
  });
  await page.goto('/users/');
  for (const action of ['Reset password', 'Disable account']) {
    const trigger = page.getByRole('button', { name: 'Actions for target@example.test' });
    await activate(page, trigger);
    await expect(page.getByRole('menu').locator('[data-autofocus]')).toBeFocused();
    await page.keyboard.press('ArrowDown');
    if (action === 'Disable account') await page.keyboard.press('ArrowDown');
    await expect(page.getByRole('menuitem', { name: action, exact: true })).toBeFocused();
    await page.keyboard.press('Enter');
    const dialog = page.getByRole('dialog', { name: action });
    await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    await expect.soft(trigger).toBeFocused();
  }
});

// A narrow screen must keep search, global sorting, pagination, and account
// creation reachable by keyboard without making the whole page overflow.
test('People directory search and pagination work on a narrow screen', async ({ page }) => {
  const people = Array.from({ length: 23 }, (_, index) => ({ ...target,
    id: `person-${index}`, displayName: `Person ${String(index + 1).padStart(2, '0')}`,
    primaryEmail: `person-${index + 1}@example.test`,
  })).reverse();
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: admin(['user.list', 'user.create']) });
    if (path.endsWith('/users')) return route.fulfill({ json: people });
  });
  await page.goto('/users/');
  await expect(page.getByRole('status')).toHaveText('Showing 1–10 of 23 users');
  await activate(page, page.getByRole('button', { name: 'Next page' }));
  await expect(page.getByRole('status')).toHaveText('Showing 11–20 of 23 users');
  await enter(page, page.getByRole('searchbox', { name: 'Search users' }), 'Person 23');
  await expect(page.getByRole('row', { name: /Person 23/ })).toBeVisible();
  await expect(page.getByRole('status')).toHaveText('Showing 1–1 of 1 user');
  await activate(page, page.getByRole('button', { name: 'Clear filters' }));
  await activate(page, page.getByRole('button', { name: 'Sort by Account' }));
  await expect(page.getByRole('columnheader', { name: 'Sort by Account' })).toHaveAttribute('aria-sort', 'descending');
  await expect(page.getByRole('row').nth(1)).toContainText('Person 23');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await activate(page, page.getByRole('button', { name: 'Add local account' }));
  await expect(page.getByRole('dialog').getByLabel('Email address')).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog')).toBeHidden();
  await expect(page.getByRole('button', { name: 'Add local account' })).toBeFocused();
});

for (const [heading, action, endpoint, fields] of [
  ['Backup', 'Create backup', '/backups', [['Destination', '/fixture/backup.age'], ['Passphrase', 'disposable fixture']]],
  ['Validate a restore', 'Validate restore', '/restores/validate', [['Archive path', '/fixture/backup.age'], ['Replacement directory', '/fixture/restore'], ['Passphrase', 'disposable fixture']]],
] as const) {
  // Invalid submission returns focus to the first field, and a valid
  // keyboard submission clears the password before showing progress.
  test(`${heading} keyboard validation and progress`, async ({ page }) => {
    const labels: [string, string, string] = ['Open request', 'Verify management data', 'Record result'];
    await fixture(page, (route, path) => {
      if (path.endsWith('/session')) return route.fulfill({ json: admin(['backup.create', 'restore.validate']) });
      if (path.endsWith(endpoint)) return route.fulfill({ status: 202, json: { id: operationID } });
    }, operation(labels));
    await page.goto('/recovery/');
    const section = page.locator('section').filter({ has: page.getByRole('heading', { name: heading, exact: true }) });
    const submit = section.getByRole('button', { name: action });
    await activate(page, submit);
    await expect(section.getByRole('textbox', { name: fields[0][0], exact: true })).toBeFocused();
    for (const [label, value] of fields) {
      await tabTo(page, label === 'Passphrase' ? section.getByLabel(label) : section.getByRole('textbox', { name: label, exact: true }));
      await page.keyboard.insertText(value);
    }
    await activate(page, submit);
    await expect(section.getByLabel('Passphrase')).toHaveValue('');
    await checkWorkflow(page, labels, section);
  });
}

// Template authors pick readable catalog names while the request retains
// the resource profile and platform artifact identifiers.
test('creates a project with named catalog choices', async ({ page }) => {
  let submitted: Record<string, string> | undefined;
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: admin(['template.create']) });
    if (path.endsWith('/templates/catalog')) return route.fulfill({ json: {
      resourceProfiles: [{ id: 'profile-fixture', name: 'Small' }],
      platformImages: [{ id: 'artifact-fixture', name: 'Ubuntu 24.04' }],
    } });
    if (path.endsWith('/templates') && route.request().method() === 'POST') {
      submitted = route.request().postDataJSON();
      return route.fulfill({ status: 201, json: {} });
    }
    if (path.endsWith('/templates')) return route.fulfill({ json: { items: [] } });
  });
  await page.goto('/templates/');
  await activate(page, page.getByRole('button', { name: 'New project' }));
  const dialog = page.getByRole('dialog', { name: 'New project' });
  await enter(page, dialog.getByRole('textbox', { name: 'Name', exact: true }), 'Catalog fixture');
  await enter(page, dialog.getByRole('textbox', { name: 'Repository URL' }), 'https://github.com/example/project');
  await dialog.getByRole('combobox', { name: 'Resource profile' }).selectOption({ label: 'Small' });
  await dialog.getByRole('combobox', { name: 'Platform image' }).selectOption({ label: 'Ubuntu 24.04' });
  await activate(page, dialog.getByRole('button', { name: 'Create project' }));
  await expect(dialog).toBeHidden();
  expect(submitted).toMatchObject({ resourceProfileId: 'profile-fixture', platformArtifactId: 'artifact-fixture' });
});

// Template details and action dialogs remain reachable on narrow screens;
// cancel returns focus to the menu trigger, and removing a row has a stable target.
test('project inventory keyboard dialogs and archive focus', async ({ page }) => {
  let archived = false;
  const project = { id: templateID, name: 'Keyboard project', ownerId: 'admin', ownerName: 'Project author',
    currentRevision: 2, version: 4, state: 'active', repositoryUrl: 'https://github.com/example/project',
    devcontainerPath: '.devcontainer/devcontainer.json', resourceProfileName: 'Small', platformImageName: 'Ubuntu release', secretNames: [] };
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: admin(['template.create']) });
    if (path.endsWith(`/templates/${templateID}`) && route.request().method() === 'PATCH') {
      archived = true;
      return route.fulfill({ json: { ...project, state: 'archived' } });
    }
    if (path.endsWith('/templates')) return route.fulfill({ json: { items: archived ? [] : [project] } });
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/templates/');
  const actions = page.getByRole('button', { name: 'Actions for Keyboard project' });
  await activate(page, actions);
  await expect(page.getByRole('menu').locator('[data-autofocus]')).toBeFocused();
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('End');
  await expect(page.getByRole('menuitem', { name: 'Archive project' })).toBeFocused();
  await page.keyboard.press('ArrowUp');
  await expect(page.getByRole('menuitem', { name: 'View details' })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('dialog', { name: 'Project details' })).toContainText('Small');
  await activate(page, page.getByRole('button', { name: 'Done', exact: true }));
  await expect(actions).toBeFocused();
  await activate(page, actions);
  await expect(page.getByRole('menu').locator('[data-autofocus]')).toBeFocused();
  await page.keyboard.press('ArrowDown');
  await expect(page.getByRole('menuitem', { name: 'New revision' })).toBeFocused();
  await page.keyboard.press('End');
  await expect(page.getByRole('menuitem', { name: 'Archive project' })).toBeFocused();
  await page.keyboard.press('Enter');
  const dialog = page.getByRole('dialog', { name: 'Archive project' });
  await expect(dialog.getByRole('button', { name: 'Cancel' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(actions).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('menu').locator('[data-autofocus]')).toBeFocused();
  await page.keyboard.press('ArrowDown');
  await expect(page.getByRole('menuitem', { name: 'New revision' })).toBeFocused();
  await page.keyboard.press('End');
  await page.keyboard.press('Enter');
  await activate(page, dialog.getByRole('button', { name: 'Archive project' }));
  await expect(page.getByText('No projects yet.')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Projects', exact: true })).toBeFocused();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

// A recovery reviewer advances through gated decisions by keyboard and can
// inspect the completed activation step after authority changes.
test('recovery review and activation', async ({ page }) => {
  const reviewPath = `/api/v1/recovery/${reviewID}`;
  let state = 'identity_review';
  let version = 1;
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: {
      ...admin(['recovery.review']), mode: 'recovery_review', installationState: 'recovery', recoveryReviewId: reviewID,
    } });
    if (path === `${reviewPath}/activate`) return route.fulfill({ status: 202, json: { id: operationID, status: 'succeeded', steps: [
      { key: 'activate', index: 0, label: 'Activate reviewed installation', status: 'succeeded', detail: 'Activation recorded.' },
    ] } });
    if (path !== reviewPath) return;
    if (route.request().method() === 'PATCH') {
      const { section } = route.request().postDataJSON();
      state = section === 'identities' ? 'provider_reconcile' : section === 'provider' ? 'authorization_review' : 'ready';
      version++;
    }
    return route.fulfill({ json: { id: reviewID, state, version, restoreOperationId: operationID,
      sections: { identities: [{ ...target, primaryEmail: 'reviewer@example.test' }], workspaces: [] } } });
  });
  await page.goto('/recovery/');
  await expect(page.getByRole('button', { name: /: In progress$/ })).toHaveAttribute('aria-current', 'step');
  await activate(page, page.getByRole('checkbox', { name: 'reviewer@example.test' }), 'Space');
  await activate(page, page.getByRole('button', { name: 'Confirm identities' }));
  await expect(page.getByRole('heading', { name: 'Recovery review' })).toBeFocused();
  await activate(page, page.getByRole('button', { name: 'Check provider inventory' }));
  await expect(page.getByRole('heading', { name: 'Recovery review' })).toBeFocused();
  await activate(page, page.getByRole('checkbox', { name: /approve resuming the configured retention policy/i }), 'Space');
  await activate(page, page.getByRole('button', { name: 'Confirm authorization and retention' }));
  await expect(page.getByRole('heading', { name: 'Recovery review' })).toBeFocused();
  await activate(page, page.getByRole('button', { name: 'Activate installation' }));
  await expect(page.getByRole('heading', { name: 'Recovery complete' })).toBeFocused();
  await activate(page, page.getByRole('button', { name: 'Activate reviewed installation: Completed' }));
  await expect(page.getByText('Activation recorded.')).toBeVisible();
});

// Personal settings and sign-out are keyboard accessible from the header;
// cancelling a password edit returns focus without changing credentials.
test('personal settings avatar menu and password dialog', async ({ page }) => {
  let signedOut = false;
  let mutations = 0;
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) {
      if (route.request().method() === 'DELETE') {
        signedOut = true;
        return route.fulfill({ status: 204 });
      }
      if (signedOut) return route.fulfill({ status: 401, json: {} });
      return route.fulfill({ json: { ...admin(['session.logout', 'session.change_password', 'git.manage', 'secret.manage_user']),
        user: { id: 'admin', role: 'administrator', displayName: 'Alex Developer', primaryEmail: 'alex@example.test', credentialMode: 'local' },
      } });
    }
    if (path.endsWith('/session/password')) {
      mutations++;
      return route.fulfill({ status: 500, json: {} });
    }
    if (path.endsWith('/git/authorization')) return route.fulfill({ json: { state: 'connected' } });
    if (path.endsWith('/secrets')) return route.fulfill({ json: [] });
  });
  await page.goto('/account/');
  const avatar = page.getByRole('button', { name: 'User menu' });
  await expect(page.getByRole('banner').getByRole('button', { name: 'User menu' })).toBeVisible();
  await activate(page, avatar);
  await expect(page.getByRole('menu').locator('[data-autofocus]')).toBeFocused();
  await expect(page.getByRole('menu')).toContainText('alex@example.test');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.keyboard.press('ArrowDown');
  await expect(page.getByRole('menuitem', { name: 'Personal settings' })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('heading', { name: 'Personal settings', exact: true })).toBeVisible();
  const change = page.getByRole('button', { name: 'Change password', exact: true });
  await activate(page, change);
  const dialog = page.getByRole('dialog', { name: 'Change password' });
  await expect(dialog.getByLabel(/^Current password/)).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(change).toBeFocused();
  expect(mutations).toBe(0);
  await activate(page, avatar);
  await expect(page.getByRole('menu').locator('[data-autofocus]')).toBeFocused();
  await page.keyboard.press('ArrowDown');
  await page.keyboard.press('ArrowDown');
  await expect(page.getByRole('menuitem', { name: 'Sign out' })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('heading', { name: 'Log in to ShiftBox' })).toBeVisible();
  await expect(page.getByText('Your session expired.')).toBeHidden();
  await expect(avatar).toBeHidden();
  await expect(page.getByRole('region', { name: 'Your profile' })).toBeHidden();
});

// Workspaces is home from both the site root and the shared header logo.
test('opens Workspaces from the root and the ShiftBox logo', async ({ page }) => {
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: admin([]) });
    if (path.endsWith('/workspaces') || path.endsWith('/templates')) return route.fulfill({ json: { items: [], nextCursor: null } });
  });
  await page.goto('/');
  await expect(page).toHaveURL(/\/workspaces\/$/);
  await page.goto('/templates/');
  await activate(page, page.getByRole('link', { name: 'ShiftBox home' }));
  await expect(page).toHaveURL(/\/workspaces\/$/);
  await expect(page.getByRole('heading', { name: 'Workspaces', exact: true })).toBeVisible();
});

// A controller denial after the login deadline takes the user straight to the
// branded credential form and explains why access ended.
test('expired login opens the login form with a banner', async ({ page }) => {
  const expiresAt = new Date(Date.now() + 60_000).toISOString();
  let expired = false;
  await fixture(page, (route, path) => {
    if (path.endsWith('/session') && !expired) return route.fulfill({ json: { ...admin(['session.logout']), expiresAt } });
    if (path.endsWith('/session') || (path.endsWith('/workspaces') && expired)) {
      return route.fulfill({ status: 401, contentType: 'application/problem+json',
        body: JSON.stringify({ type: 'about:blank', title: 'Sign in to continue', status: 401, code: 'auth.required', requestId: '' }),
      });
    }
    if (path.endsWith('/workspaces')) return route.fulfill({ json: { items: [], nextCursor: null } });
  });
  await page.goto('/workspaces/');
  await expect(page.getByText('No workspaces yet.')).toBeVisible();
  expired = true;
  await page.clock.setSystemTime(new Date(Date.parse(expiresAt) + 1));
  await activate(page, page.getByRole('button', { name: 'Refresh' }));
  await expect(page.getByRole('heading', { name: 'Log in to ShiftBox' })).toBeVisible();
  await expect(page.locator('meta[name="theme-color"]')).toHaveAttribute('content', '#4834F5');
  await expect(page.getByRole('alert')).toHaveText('Your session expired. Log in again to continue.');
});

// A password typo stays in the dialog with an accessible field error;
// only matching entries reach the API, without the confirmation value.
test('password confirmation blocks mismatched changes', async ({ page }) => {
  let submitted: Record<string, string> | undefined;
  await fixture(page, (route, path) => {
    if (path.endsWith('/session')) return route.fulfill({ json: { ...admin(['session.change_password']),
      user: { id: 'admin', role: 'administrator', credentialMode: 'local' },
    } });
    if (path.endsWith('/session/password')) {
      submitted = route.request().postDataJSON();
      return route.fulfill({ status: 204 });
    }
  });
  await page.goto('/account/');
  await activate(page, page.getByRole('button', { name: 'Change password', exact: true }));
  const dialog = page.getByRole('dialog', { name: 'Change password' });
  await enter(page, dialog.getByLabel(/^Current password/), 'current-fixture');
  await enter(page, dialog.getByLabel(/^New password/), 'replacement-fixture');
  const confirmation = dialog.getByLabel(/^Confirm password/);
  await enter(page, confirmation, 'replacement-fixture-typo');
  await activate(page, dialog.getByRole('button', { name: 'Change password' }));
  await expect(confirmation).toBeFocused();
  await expect(confirmation).toHaveAttribute('aria-invalid', 'true');
  await expect(dialog.getByText('Passwords do not match. Re-enter your new password.')).toBeVisible();
  expect(submitted).toBeUndefined();
  await confirmation.fill('replacement-fixture');
  await activate(page, dialog.getByRole('button', { name: 'Change password' }));
  await expect(page.getByRole('link', { name: 'Sign in', exact: true })).toBeVisible();
  expect(submitted).toEqual({ currentPassword: 'current-fixture', newPassword: 'replacement-fixture' });
});
