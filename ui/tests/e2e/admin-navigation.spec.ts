import { expect, test } from '@playwright/test';

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
