import { readFile } from 'node:fs/promises';
import { isAbsolute } from 'node:path';
import { expect, test, type Locator, type Page } from '@playwright/test';

type InstallationFixture = {
  setupURL: string;
  managementURL: string;
  administratorEmail: string;
  temporaryPassword: string;
  password: string;
  postgresURI?: string;
  certificateChainPath: string;
  privateKeyPath: string;
  ideSuffix: string;
  previewSuffix: string;
  capacity: { cpuCount: number; memoryMiB: number; diskMiB: number };
  workspaceFixturePath: string;
};

type WorkspaceFixture = {
  artifactID: string;
  profileID: string;
  repositoryURL: string;
  repositoryRef?: string;
  devcontainerPath: string;
  // Keep this file at the repository root so the workbench can select it by name.
  markerFile: string;
  originalDevcontainerPath: string;
};

const fixturePath = process.env.SHIFTBOX_MVP_FIXTURE;
const startupTimeout = 4 * 60_000;
const workbenchTimeout = 3 * 60_000;

// This fixture contains disposable passwords. Keep reproductions and
// recordings on the fixture host, with browser capture disabled.
test.use({ channel: 'chrome', trace: 'off', screenshot: 'off', video: 'off' });
test.skip(!fixturePath, 'Set SHIFTBOX_MVP_FIXTURE for the disposable UTM journey');
test.skip(({ browserName }) => browserName !== 'chromium', 'The exact-stack MVP journey uses Chrome');

// Start setup with a fresh installation and locally trusted bootstrap TLS.
// Use normal browser trust and DNS for all HTTPS origins. For ide.example.com
// the management certificate must cover the management host, *.ide.example.com,
// *.repair.example.com, and the configured preview wildcard.
// The runner starts the controller on the same installation after activation
// and writes workspaceFixturePath when the artifact and profile are ready.
test('MVP setup, reconnect, retained checkout, and repair', async ({ page }) => {
  test.setTimeout(20 * 60_000);
  if (!fixturePath || !isAbsolute(fixturePath)) throw new Error('SHIFTBOX_MVP_FIXTURE must be an absolute path');
  const installation: InstallationFixture = JSON.parse(await readFile(fixturePath, 'utf8'));
  const setupOrigin = new URL(installation.setupURL).origin;
  const managementOrigin = new URL(installation.managementURL).origin;
  if (!isAbsolute(installation.workspaceFixturePath) || new URL(setupOrigin).hostname !== '127.0.0.1' ||
      !setupOrigin.startsWith('https://') || !managementOrigin.startsWith('https://') || setupOrigin === managementOrigin) {
    throw new Error('Use a protected loopback setup URL and separate HTTPS management origin');
  }

  await test.step('Complete protected setup with the keyboard and sign in afresh', async () => {
    await page.goto(new URL('/setup/', setupOrigin).href);
    await expect(page.getByRole('heading', { name: 'Local setup' })).toBeVisible();
    await activate(page.getByRole('link', { name: 'Sign in', exact: true }));
    await passwordLogin(page, installation.administratorEmail, installation.temporaryPassword);
    await activate(page.getByRole('button', { name: 'Change password' }));
    await expect(page.getByLabel(/^Current password/)).toBeFocused();
    await enter(page.getByLabel(/^Current password/), installation.temporaryPassword);
    await enter(page.getByLabel(/^New password/), installation.password);
    await activate(page.getByRole('button', { name: 'Change password' }));
    await activate(page.getByRole('link', { name: 'Sign in', exact: true }));
    await passwordLogin(page, installation.administratorEmail, installation.password);
    if (installation.postgresURI) {
      await activate(page.getByRole('radio', { name: 'PostgreSQL for production' }), 'Space');
      await enter(page.getByLabel('PostgreSQL connection URI'), installation.postgresURI);
    } else {
      await activate(page.getByRole('radio', { name: 'SQLite for a small or demo installation' }), 'Space');
    }
    await activate(page.getByRole('button', { name: 'Validate database' }));
    await enter(page.getByLabel('Management address'), managementOrigin);
    await enter(page.getByLabel('IDE domain suffix'), installation.ideSuffix);
    await enter(page.getByLabel('Preview domain suffix'), installation.previewSuffix);
    // Playwright supplies native file-picker selections without a desktop
    // dialog. The upload form and all submissions still use the real UI.
    await page.locator('input[type=file]').nth(0).setInputFiles(installation.certificateChainPath);
    await page.locator('input[type=file]').nth(1).setInputFiles(installation.privateKeyPath);
    await activate(page.getByRole('button', { name: 'Validate certificate' }));
    await activate(page.getByRole('button', { name: 'Validate platform image' }));
    await expect(page.getByRole('heading', { name: 'Platform image', exact: true })).toBeHidden({ timeout: startupTimeout });
    await enter(page.getByLabel('vCPU limit'), String(installation.capacity.cpuCount));
    await enter(page.getByLabel('Memory limit (MiB)'), String(installation.capacity.memoryMiB));
    await enter(page.getByLabel('Disk limit (MiB)'), String(installation.capacity.diskMiB));
    await activate(page.getByRole('button', { name: 'Save capacity' }));
    await activate(page.getByRole('checkbox', { name: 'I understand that I will need to sign in again' }), 'Space');
    await activate(page.getByRole('button', { name: 'Activate installation' }));
    await expect(page.getByText(/^Setup is complete\./)).toBeVisible();
    const sessionURL = new URL('/api/v1/session', managementOrigin).href;
    await expect.poll(async () => {
      try {
        return (await page.request.get(sessionURL, { timeout: 5_000 })).status();
      } catch {
        return 0;
      }
    }, { timeout: startupTimeout, message: 'The active controller must require a new sign-in' }).toBe(401);
    await page.goto(new URL('/auth/login', managementOrigin).href);
    await passwordLogin(page, installation.administratorEmail, installation.password);
  });

  let workspaceFixture: WorkspaceFixture | undefined;
  await expect.poll(async () => {
    try {
      workspaceFixture = JSON.parse(await readFile(installation.workspaceFixturePath, 'utf8'));
      return Boolean(workspaceFixture?.artifactID && workspaceFixture.profileID);
    } catch {
      return false;
    }
  }, { timeout: startupTimeout, message: 'The workspace runner must provide the pinned artifact and profile IDs' }).toBe(true);
  if (!workspaceFixture) throw new Error('Workspace fixture unavailable');
  const fixture: WorkspaceFixture = workspaceFixture;
  if (!isAbsolute(fixture.originalDevcontainerPath)) throw new Error('Supply the local copy of the fixture Dev Container configuration');
  const savedConfig = await readFile(fixture.originalDevcontainerPath, 'utf8');
  const templateName = `MVP journey ${Date.now()}`;
  const marker = `shiftbox-retained-${Date.now()}`;
  let workspaceURL = '';

  await test.step('Authorize GitHub and create a template', async () => {
    await page.goto(new URL('/account/', managementOrigin).href);
    const connected = page.getByText('GitHub is connected.', { exact: true });
    const connect = page.getByRole('button', { name: /^(Connect|Reconnect) GitHub$/ });
    await expect(connected.or(connect)).toBeVisible({ timeout: startupTimeout });
    if (!await connected.isVisible()) {
      await activate(connect);
      await activate(page.getByRole('link', { name: 'Continue to GitHub' }));
      // The disposable GitHub App owner can grant consent in a headed run.
      await page.waitForURL(url => url.origin === managementOrigin && url.pathname === '/', { timeout: startupTimeout });
      await page.goto(new URL('/account/', managementOrigin).href);
      await expect(connected).toBeVisible({ timeout: startupTimeout });
    }
    await page.goto(new URL('/templates/', managementOrigin).href);
    await activate(page.getByRole('button', { name: 'New template' }));
    const form = page.getByRole('dialog', { name: 'New template' });
    await enter(form.getByLabel('Name', { exact: true }), templateName);
    await enter(form.getByLabel('Repository URL'), fixture.repositoryURL);
    if (fixture.repositoryRef) await enter(form.getByLabel('Repository ref'), fixture.repositoryRef);
    await enter(form.getByLabel('Dev Container configuration'), fixture.devcontainerPath);
    await enter(form.getByLabel('Resource profile ID'), fixture.profileID);
    await enter(form.getByLabel('Platform image ID'), fixture.artifactID);
    await activate(form.getByRole('button', { name: 'Create template' }));
    await expect(form).toBeHidden();
  });

  await test.step('Reconnect workflow events and withhold IDE until verification', async () => {
    let streamAttempts = 0;
    await page.route('**/api/v1/operations/*/events', async route => {
      streamAttempts++;
      if (streamAttempts === 1) await route.abort('connectionreset');
      else await route.continue();
    });
    await page.goto(new URL('/workspaces/?launch=1', managementOrigin).href);
    await activate(page.getByRole('radio', { name: `${templateName} · Revision 1` }), 'Space');
    await activate(page.getByRole('button', { name: 'Review launch' }));
    await expect(page.getByRole('heading', { name: `${templateName} · Revision 1` })).toBeVisible();
    await activate(page.getByRole('button', { name: 'Launch workspace' }));
    const details = page.getByRole('link', { name: 'View workspace' });
    await expect(details).toBeVisible();
    workspaceURL = new URL((await details.getAttribute('href')) ?? '', managementOrigin).href;

    // The launch page remains attached to its stream while another tab
    // verifies the management entry control cannot open an unready IDE.
    const early = await page.context().newPage();
    try {
      await early.goto(workspaceURL);
      await expect(early.getByRole('heading', { name: 'Workspace', exact: true })).toBeVisible();
      await expect(early.getByRole('button', { name: /^Verify the browser IDE: (Pending|In progress)$/ })).toBeVisible();
      await expect(early.getByRole('button', { name: 'Open IDE', exact: true })).toHaveCount(0);
      await expect.poll(() => streamAttempts, { timeout: startupTimeout }).toBeGreaterThanOrEqual(2);
      await stepCompleted(page, 'Verify the browser IDE');
      await expect(early.getByRole('button', { name: 'Open IDE', exact: true })).toBeVisible({ timeout: startupTimeout });
      await stepCompleted(page, 'Finish remaining setup hooks');
    } finally {
      await early.close();
      await page.unroute('**/api/v1/operations/*/events');
    }
    await activate(details);
  });

  await test.step('Save a file and retain it across stop, start, and rebuild', async () => {
    const normalOrigin = await openIDE(page, 'Open IDE', managementOrigin);
    const markerEditor = await openFile(page, fixture.markerFile);
    await writeEditor(page, markerEditor, marker);
    await page.goto(workspaceURL);
    await activate(page.getByRole('button', { name: 'Stop workspace' }));
    await stepCompleted(page, 'Stop workspace');
    await expect(page.getByRole('button', { name: 'Open IDE', exact: true })).toHaveCount(0);
    await activate(page.getByRole('button', { name: 'Start workspace' }));
    await stepCompleted(page, 'Finish remaining setup hooks');
    expect(await openIDE(page, 'Open IDE', managementOrigin)).toBe(normalOrigin);
    await expect((await openFile(page, fixture.markerFile)).getByText(marker, { exact: true })).toBeVisible();
    await page.goto(workspaceURL);
    await activate(page.getByRole('button', { name: 'Rebuild container' }));
    await stepCompleted(page, 'Rebuild the development container');
    await stepCompleted(page, 'Finish remaining setup hooks');
    expect(await openIDE(page, 'Open IDE', managementOrigin)).toBe(normalOrigin);
    await expect((await openFile(page, fixture.markerFile)).getByText(marker, { exact: true })).toBeVisible();
  });

  await test.step('Repair a broken project and retry setup', async () => {
    const broken = '{"image":"invalid image reference for repair fixture"}';
    await writeEditor(page, await openFile(page, fixture.devcontainerPath), broken);
    await page.goto(workspaceURL);
    await activate(page.getByRole('button', { name: 'Rebuild container' }));
    await expect(page.getByRole('button', { name: 'Rebuild the development container: Failed' }))
      .toBeVisible({ timeout: startupTimeout });
    await expect(page.getByRole('button', { name: 'Open IDE', exact: true })).toHaveCount(0);
    await activate(page.getByRole('button', { name: 'Start repair' }));
    await stepCompleted(page, 'Verify the repair IDE');
    const repairOrigin = await openIDE(page, 'Open repair IDE', managementOrigin);
    expect(new URL(repairOrigin).hostname).toMatch(/^[a-f0-9-]+\.repair\./);
    await expect((await openFile(page, fixture.markerFile)).getByText(marker, { exact: true })).toBeVisible();
    // The fixture is plain JSON. Restore the equivalent configuration as
    // one line so the editor's automatic indentation cannot change it.
    await writeEditor(page, await openFile(page, fixture.devcontainerPath), JSON.stringify(JSON.parse(savedConfig)));
    await page.goto(workspaceURL);
    await activate(page.getByRole('button', { name: 'Exit repair and retry setup' }));
    await stepCompleted(page, 'Exit repair and retry development setup');
    await stepCompleted(page, 'Verify the browser IDE');
    await stepCompleted(page, 'Finish remaining setup hooks');
    expect(await openIDE(page, 'Open IDE', managementOrigin)).not.toBe(repairOrigin);
    await expect((await openFile(page, fixture.markerFile)).getByText(marker, { exact: true })).toBeVisible();
  });
});

async function activate(control: Locator, key = 'Enter') {
  await expect(control).toBeEnabled({ timeout: startupTimeout });
  await control.press(key, { timeout: startupTimeout });
}

async function enter(control: Locator, value: string) {
  await control.press('ControlOrMeta+A');
  await control.pressSequentially(value);
}

async function passwordLogin(page: Page, email: string, password: string) {
  // Dex shows the connector chooser only when more than one method is enabled.
  const username = page.locator('input[name=login]');
  const passwordConnector = page.getByRole('button', { name: /Log in with Email/i })
    .or(page.getByRole('link', { name: /Log in with Email/i }));
  await expect(username.or(passwordConnector)).toBeVisible();
  if (await passwordConnector.isVisible()) await activate(passwordConnector);
  await enter(username, email);
  await enter(page.locator('input[name=password]'), password);
  await activate(page.getByRole('button', { name: 'Login', exact: true }));
  // The pinned Dex may require consent before returning to ShiftBox.
  await page.waitForURL(url => !url.pathname.startsWith('/dex/') || url.pathname === '/dex/approval');
  if (new URL(page.url()).pathname === '/dex/approval') {
    await activate(page.getByRole('button', { name: 'Grant Access', exact: true }));
  }
}

async function stepCompleted(page: Page, label: string) {
  await expect(page.getByRole('button', { name: `${label}: Completed`, exact: true })).toBeVisible({ timeout: startupTimeout });
}

async function openIDE(page: Page, button: string, managementOrigin: string) {
  await activate(page.getByRole('button', { name: button, exact: true }));
  await page.waitForURL(url => url.protocol === 'https:' && url.origin !== managementOrigin, { timeout: workbenchTimeout });
  await expect(page.locator('.monaco-workbench')).toBeVisible({ timeout: workbenchTimeout });
  return new URL(page.url()).origin;
}

async function openFile(page: Page, name: string) {
  const explorer = page.getByRole('tree', { name: 'Files Explorer' });
  const path = name.split('/');
  const file = path.pop();
  for (const [index, folder] of path.entries()) {
    const item = explorer.getByRole('treeitem', { name: folder, exact: true });
    await expect(item).toBeVisible({ timeout: workbenchTimeout });
    const child = explorer.getByRole('treeitem', { name: path[index + 1] ?? file, exact: true });
    if (!await child.isVisible()) await item.locator('.monaco-tl-twistie').click();
  }
  const item = explorer.getByRole('treeitem', { name: file, exact: true });
  await expect(item).toBeVisible({ timeout: workbenchTimeout });
  await item.dblclick();
  await expect(page.getByRole('tab', { name: file, exact: false, selected: true })).toBeVisible();
  const editor = page.locator('.editor-instance .monaco-editor:visible').first();
  await expect(editor).toBeVisible();
  return editor;
}

async function writeEditor(page: Page, editor: Locator, content: string) {
  await editor.locator('.view-lines').click();
  await page.keyboard.press('Escape');
  await page.keyboard.press('ControlOrMeta+a');
  await page.keyboard.press('Backspace');
  // A Monaco selection can stop inside JSON. Re-select after the first
  // deletion and verify the buffer is empty before entering new content.
  if ((await editor.locator('.view-lines').innerText()).trim() !== '') {
    await page.keyboard.press('ControlOrMeta+a');
    await page.keyboard.press('Backspace');
  }
  await expect(editor.locator('.view-lines')).toHaveText(/^\s*$/);
  await page.keyboard.insertText(content);
  await page.keyboard.press('ControlOrMeta+s');
  await expect(page.locator('.tabs-container .tab.active')).not.toHaveClass(/\bdirty\b/);
}
