import { expect, test } from '@playwright/test';
import { expectReadableText } from './text-contrast';

// The browser starts in the system theme, then an explicit header choice
// survives reloads and later system changes while keeping the matching logo.
test('persists the header theme choice in browser storage', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route('**/api/v1/**', route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/v1/session') return route.fulfill({ json: {
      user: { id: 'theme-review', primaryEmail: 'review@example.test' }, role: 'administrator',
      mode: 'normal', installationState: 'active', permissions: ['infrastructure.view'],
    } });
    if (path === '/api/v1/admin/infrastructure') return route.fulfill({ json: {
      provider: { name: 'utm', version: 'fixture', architecture: 'amd64' },
      capacity: { cpuCount: 0, memoryMiB: 0, diskMiB: 0 }, version: 1, conditions: [],
    } });
    if (path === '/api/v1/admin/resource-profiles') return route.fulfill({ json: [] });
    return route.fulfill({ status: 404, json: {} });
  });
  await page.goto('/infrastructure/');
  await expect(page.locator('meta[name="theme-color"]')).toHaveAttribute('content', '#4834F5');
  const toggle = page.getByRole('button', { name: 'Dark mode' });
  await expect(toggle).toHaveAttribute('aria-pressed', 'true');
  await expect(page.getByAltText('ShiftBox')).toHaveAttribute('src', '/brand/shiftbox-logo-dark.svg');
  expect(await page.evaluate(() => localStorage.getItem('mantine-color-scheme-value'))).toBeNull();
  await toggle.focus();
  await page.keyboard.press('Enter');
  await expect(toggle).toHaveAttribute('aria-pressed', 'false');
  await expect(page.locator('html')).toHaveAttribute('data-mantine-color-scheme', 'light');
  expect(await page.evaluate(() => localStorage.getItem('mantine-color-scheme-value'))).toBe('light');
  await page.reload();
  await expect(toggle).toHaveAttribute('aria-pressed', 'false');
  await expect(page.getByAltText('ShiftBox')).toHaveAttribute('src', '/brand/shiftbox-logo-light.svg');
  await toggle.focus();
  await page.keyboard.press('Space');
  await expect(toggle).toHaveAttribute('aria-pressed', 'true');
  await page.emulateMedia({ colorScheme: 'light' });
  await page.reload();
  await expect(toggle).toHaveAttribute('aria-pressed', 'true');
  await expect(page.getByAltText('ShiftBox')).toHaveAttribute('src', '/brand/shiftbox-logo-dark.svg');
  expect(await page.evaluate(() => localStorage.getItem('mantine-color-scheme-value'))).toBe('dark');
  await page.setViewportSize({ width: 320, height: 700 });
  const avatar = page.getByRole('button', { name: 'User menu' });
  await expect(toggle).toBeVisible();
  await expect(avatar).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

for (const scheme of ['light', 'dark'] as const) {
  // The workspace description must be legible against its rendered surface in either theme.
  test(`workspace description has sufficient contrast in ${scheme} mode`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    await page.route('**/api/v1/**', route => {
      const path = new URL(route.request().url()).pathname;
      if (path === '/api/v1/session') return route.fulfill({ json: {
        user: { id: 'theme-review', role: 'member' }, mode: 'normal', installationState: 'active', permissions: [],
      } });
      if (path === '/api/v1/workspaces') return route.fulfill({ json: { items: [], nextCursor: null } });
      return route.fulfill({ status: 404, json: {} });
    });
    await page.goto('/workspaces/');
    await expect(page.locator('html')).toHaveAttribute('data-mantine-color-scheme', scheme);
    await expect(page.getByAltText('ShiftBox')).toHaveAttribute('src', `/brand/shiftbox-logo-${scheme}.svg`);
    await expectReadableText(page.getByText('Your development environments, resources, and lifecycle status.'));
  });
}
