import { createHash, randomUUID } from 'node:crypto';
import { readFile, writeFile } from 'node:fs/promises';
import { isAbsolute } from 'node:path';
import { expect, test, type Locator, type Page, type WebSocket } from '@playwright/test';

type Account = { email: string; password: string };
type Terminal = { input: Locator; output: Locator };
type Session = {
  user: { id: string; primaryEmail: string; role: string };
  csrfToken: string;
  mode: string;
  installationState: string;
  organizationSecretScopeId?: string;
};
type SharingFixture = {
  disposable: true;
  managementURL: string;
  administrator: Account;
  member: Account;
  templateName: string;
  workspaceID: string;
  repositoryURL: string;
};

const fixturePath = process.env.SHIFTBOX_SHARING_FIXTURE;
const resultPath = process.env.SHIFTBOX_SHARING_RESULT;
const workbenchTimeout = 120_000;

test.use({ channel: 'chrome', trace: 'off', screenshot: 'off', video: 'off' });
test.skip(!fixturePath, 'Set SHIFTBOX_SHARING_FIXTURE for a dedicated disposable sharing installation');
test.skip(({ browserName }) => browserName !== 'chromium', 'The native sharing journey uses Chrome');

// Use a fresh, separate fixture with two connected GitHub accounts, an active
// admin-owned template, and a ready member-owned workspace launched from that
// template. Revoke its template launch grant before running this test, and
// seed an exact workspace.connect_admin resource grant for the administrator.
// The repository must be private and covered for both fixture GitHub accounts.
test('shares a project while preserving workspace and guest owner authority', async ({ browser }) => {
  test.setTimeout(12 * 60_000);
  if (resultPath && !isAbsolute(resultPath)) throw new Error('SHIFTBOX_SHARING_RESULT must be an absolute file path');
  const fixture = await readFixture();
  const origin = new URL(fixture.managementURL).origin;
  const workspacePath = `/workspaces/?id=${fixture.workspaceID}`;
  const workspaceAPI = `/api/v1/workspaces/${fixture.workspaceID}`;
  const secretKey = `SHARING_${randomUUID().replaceAll('-', '').toUpperCase()}`;
  const projectOnlyKey = `${secretKey}_PROJECT`;
  const values = { organization: randomUUID(), project: randomUUID(), member: randomUUID(), administrator: randomUUID() };

  const adminContext = await browser.newContext({ baseURL: origin });
  const memberContext = await browser.newContext({ baseURL: origin });
  try {
    const administrator = await adminContext.newPage();
    const member = await memberContext.newPage();
    const adminSession = await signIn(administrator, fixture.administrator, 'administrator');
    const memberSession = await signIn(member, fixture.member, 'member');
    expect(adminSession.user.id === memberSession.user.id, 'use two distinct ShiftBox users').toBe(false);
    const organizationScopeID = adminSession.organizationSecretScopeId;
    if (!organizationScopeID) throw new Error('The fixture administrator must be able to manage organization secrets');

    const templateList = await administrator.request.get('/api/v1/templates');
    expect(templateList.status()).toBe(200);
    const templates: { items: { id: string; ownerId: string; name: string; currentRevision: number }[] } = await templateList.json();
    const template = templates.items.find(item => item.name === fixture.templateName);
    if (!template || template.ownerId !== adminSession.user.id) throw new Error('The sharing fixture needs an active project owned by its administrator');
    const templateGrantsAPI = `/api/v1/templates/${template.id}/grants`;
    const assertLaunchDenied = async () => {
      const response = await member.request.post('/api/v1/workspaces', {
        headers: { Origin: origin, 'X-CSRF-Token': memberSession.csrfToken, 'Idempotency-Key': randomUUID() },
        data: { templateId: template.id, templateRevision: template.currentRevision, name: 'Unauthorized fixture launch' },
      });
      expect(response.status()).toBe(403);
    };
    const workspace = await member.request.get(workspaceAPI);
    expect(workspace.status()).toBe(200);
    const owned = await workspace.json();
    expect(owned.ownerId).toBe(memberSession.user.id);
    expect(owned.templateId).toBe(template.id);
    expect(owned.canOpenIde).toBe(true);

    const memberIDE = await openIDE(member, workspacePath, origin);
    const guestOrigin = new URL(memberIDE.url()).origin;
    const memberTerminal = await openTerminal(memberIDE);

    await test.step('Require owner-private access and acknowledge project-secret exposure', async () => {
      const anonymous = await browser.newContext();
      try {
        expect((await anonymous.request.get(guestOrigin)).status()).toBe(403);
      } finally {
        await anonymous.close();
      }
      // A management session by itself does not open a private service.
      expect((await administrator.request.get(guestOrigin)).status()).toBe(403);
      await member.goto('/templates/');
      await expect(member.getByRole('heading', { name: 'Projects' })).toBeVisible();
      const before = await member.request.get('/api/v1/templates');
      expect(before.status()).toBe(200);
      expect((await before.json()).items.some((item: { id: string }) => item.id === template.id)).toBe(false);
      await assertLaunchDenied();

      await administrator.goto('/infrastructure/');
      await saveSecret(administrator, secretKey, values.organization);
      await expectSecretMetadata(administrator, 'organization', organizationScopeID, secretKey, values.organization);
      await administrator.goto('/templates/');
      await templateAction(administrator, fixture.templateName, 'Project secrets');
      await saveSecret(administrator, secretKey, values.project);
      await saveSecret(administrator, projectOnlyKey, values.project);
      await expectSecretMetadata(administrator, 'template', template.id, secretKey, values.project);
      const unacknowledged = await administrator.request.post(templateGrantsAPI, {
        headers: { Origin: origin, 'X-CSRF-Token': adminSession.csrfToken, 'Idempotency-Key': randomUUID() },
        data: { userId: memberSession.user.id, acknowledgeProjectSecretExposure: false },
      });
      expect(unacknowledged.status()).toBe(400);
      expect((await unacknowledged.json()).code).toBe('template.grant_invalid');
      await assertLaunchDenied();
      await verifyGuestSecret(memberTerminal, secretKey, values.organization);
      await verifyGuestSecret(memberTerminal, projectOnlyKey, undefined);

      await administrator.getByRole('button', { name: 'Back to projects' }).click();
      await templateAction(administrator, fixture.templateName, 'Share launch access');
      const share = administrator.getByRole('dialog', { name: 'Share launch access' });
      await expect(share.getByText(/authorized workspace code can retrieve project secrets/i)).toBeVisible();
      await share.getByLabel('User ID').fill(memberSession.user.id);
      const grant = share.getByRole('button', { name: 'Grant launch access' });
      await expect(grant).toBeDisabled();
      await share.getByRole('checkbox', { name: 'I acknowledge project secret access' }).check();
      await grant.click();
      await expect(share).toBeHidden();
      await templateAction(administrator, fixture.templateName, 'Share launch access');
      await expect(share.getByRole('button', { name: `Revoke launch access for ${fixture.member.email}` })).toBeVisible();
      await share.getByRole('button', { name: 'Cancel' }).click();

      await member.reload();
      await member.getByRole('button', { name: `Launch workspace for ${fixture.templateName}` }).click();
      await expect(member.getByRole('heading', { name: `${fixture.templateName} · Revision ${template.currentRevision}` })).toBeVisible();
      const projectMetadata = await member.request.get(`/api/v1/secrets?scope=template&scopeId=${encodeURIComponent(template.id)}`);
      expect(projectMetadata.status()).toBe(403);
      await verifyGuestSecret(memberTerminal, secretKey, values.project);
      await verifyGuestSecret(memberTerminal, projectOnlyKey, values.project);
    });

    await test.step('Verify explicit empty, personal precedence, and secret metadata', async () => {
      await member.goto('/account/');
      await saveSecret(member, secretKey, '');
      await expect(member.getByRole('row', { name: new RegExp(secretKey) })).toContainText('Explicitly empty');
      await verifyGuestSecret(memberTerminal, secretKey, '');
      await saveSecret(member, secretKey, values.member, true);
      await verifyGuestSecret(memberTerminal, secretKey, values.member);
      await expectSecretMetadata(member, 'user', memberSession.user.id, secretKey, values.member);
    });

    const adminStreams: WebSocket[] = [];
    const adminIDE = await test.step('Use the member owner context during an administrator IDE connection', async () => {
      await administrator.goto('/account/');
      await expect(administrator.getByText('GitHub is connected.', { exact: true })).toBeVisible();
      await expect(member.getByText('GitHub is connected.', { exact: true })).toBeVisible();
      await saveSecret(administrator, secretKey, values.administrator);
      const adminIDE = await openIDE(administrator, workspacePath, origin, ide => {
        ide.on('websocket', socket => {
          if (new URL(socket.url()).host === new URL(guestOrigin).host) adminStreams.push(socket);
        });
      });
      expect(new URL(adminIDE.url()).origin).toBe(guestOrigin);
      const adminTerminal = await openTerminal(adminIDE);
      await verifyGuestSecret(adminTerminal, secretKey, values.member);
      await terminalCheck(adminTerminal, `GIT_TERMINAL_PROMPT=0 git -c credential.helper= -c credential.helper='!shiftbox git credential' -c credential.useHttpPath=true ls-remote --exit-code '${fixture.repositoryURL}' HEAD >/dev/null 2>&1`);
      await member.getByRole('button', { name: 'Disconnect GitHub', exact: true }).click();
      await member.getByRole('button', { name: 'Confirm disconnect' }).click();
      await expect(member.getByText('GitHub is disconnected.', { exact: true })).toBeVisible();
      await administrator.reload();
      await expect(administrator.getByText('GitHub is connected.', { exact: true })).toBeVisible();
      const repo = new URL(fixture.repositoryURL).pathname.slice(1);
      // The connected administrator's OAuth grant must never replace the
      // revoked owner grant in that same administrator's open workbench.
      await terminalCheck(adminTerminal, `(printf 'protocol=https\\nhost=github.com\\npath=%s\\n\\n' '${repo}' | shiftbox git credential get >/dev/null 2>&1); [ "$?" -eq 4 ]`);
      return adminIDE;
    });

    await test.step('Preserve existing workspace access through archive, then revoke the exact permissions', async () => {
      await member.getByRole('button', { name: `Delete ${secretKey}` }).click();
      const deleteOverride = member.getByRole('dialog', { name: 'Delete secret' });
      await expect(deleteOverride.getByText(/project value will take effect/i)).toBeVisible();
      await deleteOverride.getByRole('button', { name: 'Confirm deletion' }).click();
      await expect(deleteOverride).toBeHidden();
      await verifyGuestSecret(memberTerminal, secretKey, values.project);

      await administrator.goto('/templates/');
      await templateAction(administrator, fixture.templateName, 'Archive project');
      const archive = administrator.getByRole('dialog', { name: 'Archive project' });
      await expect(archive.getByText(/existing workspaces, files, and grants are preserved/i)).toBeVisible();
      await archive.getByRole('button', { name: 'Archive project' }).click();
      await expect(archive).toBeHidden();
      await expect(administrator.getByRole('article', { name: fixture.templateName, exact: true })).toHaveCount(0);
      await assertLaunchDenied();
      await verifyGuestSecret(memberTerminal, projectOnlyKey, values.project);

      await administrator.getByLabel('Archived projects').check();
      await templateAction(administrator, fixture.templateName, 'Manage launch grants');
      const share = administrator.getByRole('dialog', { name: 'Share launch access' });
      await share.getByRole('button', { name: `Revoke launch access for ${fixture.member.email}` }).click();
      await expect(share.getByText('No launch grants.')).toBeVisible();
      await verifyGuestSecret(memberTerminal, projectOnlyKey, undefined);
      await verifyGuestSecret(memberTerminal, secretKey, values.organization);

      await administrator.goto(workspacePath);
      await administrator.getByRole('button', { name: `Revoke access for ${fixture.administrator.email}` }).click();
      const revoke = administrator.getByRole('dialog', { name: 'Revoke connection grant' });
      const openStreams = adminStreams.filter(socket => !socket.isClosed());
      expect(openStreams.length, 'the admin workbench must have a live service WebSocket').toBeGreaterThan(0);
      await revoke.getByRole('button', { name: 'Revoke access', exact: true }).click();
      await expect(revoke).toBeHidden();
      await expect.poll(() => openStreams.every(socket => socket.isClosed())).toBe(true);
      // Reload the existing service origin with its old cookie. It cannot
      // open a second connection once the exact grant is revoked.
      const previousEntry = await adminIDE.reload({ waitUntil: 'domcontentloaded' });
      expect(previousEntry?.status()).toBe(403);
      const newEntry = await administrator.request.post(`${workspaceAPI}/entry-grants`, {
        headers: { Origin: origin, 'X-CSRF-Token': adminSession.csrfToken },
        data: { service: 'ide' },
      });
      expect(newEntry.status()).toBe(403);
      await administrator.reload();
      await expect(administrator.getByRole('heading', { level: 1 })).toBeVisible();
      await expect(administrator.getByRole('button', { name: 'Open IDE', exact: true })).toHaveCount(0);
      await verifyGuestSecret(memberTerminal, secretKey, values.organization);

      const audit = await administrator.request.get(`/api/v1/admin/audit?resourceId=${fixture.workspaceID}`);
      expect(audit.status()).toBe(200);
      const events = await audit.json() as { action: string; actorId: string; metadata: { ownerId?: string } }[];
      expect(events.some(event => event.action === 'workspace.connect_admin' && event.actorId === adminSession.user.id &&
        event.metadata.ownerId === memberSession.user.id), 'admin entry is audited against the member owner context').toBe(true);
      const auditBody = await audit.text();
      expect(Object.values(values).some(value => auditBody.includes(value)), 'audit history excludes secret values').toBe(false);
    });
    if (resultPath) {
      // The Go acceptance verifier uses only identifiers, never credentials.
      await writeFile(resultPath, JSON.stringify({
        workspaceId: fixture.workspaceID,
        templateId: template.id,
        memberId: memberSession.user.id,
        administratorId: adminSession.user.id,
        secretKey,
        projectOnlyKey,
      }) + '\n', { flag: 'wx', mode: 0o600 });
    }
  } finally {
    await Promise.all([memberContext.close(), adminContext.close()]);
  }
});

async function readFixture(): Promise<SharingFixture> {
  if (!fixturePath || !isAbsolute(fixturePath)) throw new Error('SHIFTBOX_SHARING_FIXTURE must be an absolute file path');
  const fixture: SharingFixture = JSON.parse(await readFile(fixturePath, 'utf8'));
  const management = new URL(fixture.managementURL);
  if (fixture.disposable !== true || management.protocol !== 'https:' || management.username || management.password ||
      management.search || management.hash || !fixture.administrator?.email || !fixture.member?.email ||
      !fixture.administrator.password || !fixture.member.password || !fixture.templateName ||
      !/^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/.test(fixture.workspaceID) ||
      !/^https:\/\/github\.com\/[a-z0-9_.-]+\/[a-z0-9_.-]+$/i.test(fixture.repositoryURL)) {
    throw new Error('Supply a disposable HTTPS installation, two fixture accounts, a workspace UUID, and a private GitHub repository');
  }
  return fixture;
}

async function signIn(page: Page, account: Account, role: string): Promise<Session> {
  await page.goto('/auth/login');
  await expect(page.getByRole('heading', { name: 'Log in to ShiftBox' })).toBeVisible();
  await page.getByLabel('Email', { exact: true }).fill(account.email);
  await enterPassword(page.getByLabel('Password', { exact: true }), account.password);
  await page.getByRole('button', { name: 'Log in', exact: true }).click();
  await page.waitForURL(url => !url.pathname.startsWith('/dex/'));
  await expect.poll(async () => (await page.request.get('/api/v1/session')).status()).toBe(200);
  const session: Session = await (await page.request.get('/api/v1/session')).json();
  expect(session.user.role).toBe(role);
  expect(session.user.primaryEmail === account.email, 'sign in to the designated test account').toBe(true);
  expect(session.mode).toBe('normal');
  expect(session.installationState).toBe('active');
  return session;
}

async function templateAction(page: Page, name: string, action: string) {
  await page.getByRole('button', { name: `Actions for ${name}` }).click();
  await page.getByRole('menuitem', { name: action, exact: true }).click();
}

async function saveSecret(page: Page, name: string, value: string, replace = false) {
  await page.getByRole('button', { name: replace ? `Replace ${name}` : 'Add secret', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: replace ? 'Replace secret' : 'Add secret' });
  if (!replace) await dialog.getByLabel('Secret name').fill(name);
  if (value) await enterPassword(dialog.getByLabel('Secret value'), value);
  else await dialog.getByLabel('Set an explicit empty value').check();
  await dialog.getByRole('button', { name: 'Save secret' }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByRole('rowheader', { name, exact: true })).toBeVisible();
}

async function expectSecretMetadata(page: Page, scope: 'organization' | 'template' | 'user', id: string, name: string, value: string) {
  const metadata = await page.request.get(`/api/v1/secrets?scope=${scope}&scopeId=${encodeURIComponent(id)}`);
  expect(metadata.status()).toBe(200);
  const rows = await metadata.json() as Record<string, unknown>[];
  const row = rows.find(item => item.name === name);
  expect(row, `the ${scope} metadata includes the saved secret name`).toBeDefined();
  const keys = ['explicitEmpty', 'name', 'scope', 'scopeId', 'updatedAt'];
  if (scope !== 'organization') keys.push('fallbackScope');
  expect(Object.keys(row ?? {}).sort()).toEqual(keys.sort());
  expect((await metadata.text()).includes(value), `${scope} secret is absent from API output`).toBe(false);
  expect((await page.locator('body').innerText()).includes(value), `${scope} secret is absent from rendered management output`).toBe(false);
}

async function enterPassword(field: Locator, value: string) {
  // Playwright's fill diagnostics include its input. Focus first, then send
  // text through the browser keyboard with recording disabled for this test.
  await expect(field).toBeEditable();
  await field.focus();
  await field.page().keyboard.insertText(value);
}

async function openIDE(page: Page, workspacePath: string, managementOrigin: string, onOpen?: (ide: Page) => void) {
  await page.goto(workspacePath);
  const [ide] = await Promise.all([
    page.context().waitForEvent('page', { timeout: workbenchTimeout }).then(ide => {
      onOpen?.(ide);
      return ide;
    }),
    page.getByRole('button', { name: 'Open IDE', exact: true }).click({ timeout: workbenchTimeout }),
  ]);
  await ide.waitForURL(url => url.protocol === 'https:' && url.origin !== managementOrigin, { timeout: workbenchTimeout });
  await expect(ide.locator('.monaco-workbench')).toBeVisible({ timeout: workbenchTimeout });
  return ide;
}

async function openTerminal(page: Page): Promise<Terminal> {
  const screenReader = page.getByText('Screen Reader Optimized', { exact: true });
  if (!(await screenReader.isVisible())) {
    await selectWorkbenchCommand(page, 'Toggle Screen Reader Accessibility Mode');
  }
  await selectWorkbenchCommand(page, 'Terminal: Create New Terminal');
  const input = page.locator('.xterm-helper-textarea').last();
  const output = page.locator('.xterm-accessibility-tree').last();
  const trust = page.getByRole('button', { name: 'Trust Folder & Continue', exact: true });
  const prompt = /[$#](?:\s|$)/;
  await expect.poll(async () => await trust.isVisible() || prompt.test((await output.allTextContents()).join(' ')), {
    timeout: workbenchTimeout,
  }).toBe(true);
  // A terminal requires trusting the disposable checkout itself.
  if (await trust.isVisible()) await trust.click();
  await expect(input).toBeAttached({ timeout: workbenchTimeout });
  await expect(output).toContainText(prompt, { timeout: workbenchTimeout });
  return { input, output };
}

async function selectWorkbenchCommand(page: Page, name: string) {
  await page.keyboard.press('F1');
  const command = page.locator('.quick-input-widget input');
  await command.fill(`>${name}`);
  await expect(page.getByRole('option', { name, exact: false }).first()).toBeVisible({ timeout: 15_000 });
  await command.press('Enter');
  await expect(command).toBeHidden();
}

async function verifyGuestSecret(terminal: Terminal, name: string, value: string | undefined) {
  if (value === undefined) {
    await terminalCheck(terminal, `shiftbox secrets get-secret ${name} >/dev/null 2>&1; [ "$?" -eq 3 ]`);
    return;
  }
  const digest = createHash('sha256').update(value).digest('hex');
  await terminalCheck(terminal, `value=$(shiftbox secrets get-secret ${name} 2>/dev/null) && [ "$(printf '%s' "$value" | sha256sum | cut -d ' ' -f 1)" = '${digest}' ]; status=$?; unset value; [ "$status" -eq 0 ]`);
}

async function terminalCheck({ input, output }: Terminal, check: string) {
  const marker = randomUUID().replaceAll('-', '').slice(0, 10);
  await input.focus();
  // Only the status marker reaches the terminal. Split it in the shell
  // command so echoed input cannot satisfy the assertion.
  const keyboard = input.page().keyboard;
  await keyboard.type(`( ${check} ); printf 'SHIFTBOX_%s_RC_%s\\n' '${marker}' "$?"`);
  await keyboard.press('Enter');
  await expect(output).toContainText(new RegExp(`SHIFTBOX_${marker}_RC_\\d+`), { timeout: workbenchTimeout });
  expect(new RegExp(`SHIFTBOX_${marker}_RC_0\\b`).test((await output.textContent()) ?? ''), 'terminal check exited successfully').toBe(true);
}
