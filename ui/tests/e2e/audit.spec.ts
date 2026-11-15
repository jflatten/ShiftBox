import { expect, test } from '@playwright/test';

for (const width of [1280, 390]) {
  // Audit remains readable on narrow screens; keyboard expansion exposes exact
  // evidence, and applying an event filter preserves the server's pagination.
  test(`audit history controls and details at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    const resourceId = 'd3f0eb62-f2d1-4811-a37a-4a992e8c75e8';
    const event = { id: 'one', occurredAt: '2026-11-15T10:00:00Z', actorType: 'user', actorId: 'admin',
      action: 'user.approve', resourceType: 'user', resourceId, outcome: 'allowed', reasonCode: 'membership_approved',
      metadata: { unknown: 'do not render arbitrary metadata' } };
    const writes: string[] = [];
    await page.route('**/api/v1/**', route => {
      const request = route.request();
      const url = new URL(request.url());
      if (request.method() !== 'GET') writes.push(request.method());
      if (url.pathname.endsWith('/session')) return route.fulfill({ json: {
        user: { id: 'admin', role: 'administrator', displayName: 'Demo administrator', primaryEmail: 'admin@example.test' },
        mode: 'normal', installationState: 'active', csrfToken: 'fixture', permissions: ['audit.list', 'user.list'],
      } });
      if (url.pathname.endsWith('/users')) return route.fulfill({ json: [
        { id: resourceId, displayName: 'Morgan Lee', primaryEmail: 'morgan@example.test' },
      ] });
      if (url.pathname.endsWith('/audit')) {
        if (url.searchParams.has('cursor')) return route.fulfill({ json: [{ ...event, id: 'older', reasonCode: 'older_event' }] });
        const items = url.searchParams.get('action') ? [event] : [event, { ...event, id: 'two', action: 'user.disable', outcome: 'denied' }];
        return route.fulfill({ json: items, headers: { Link: '</api/v1/admin/audit?action=user.approve&cursor=12>; rel="next"' } });
      }
      return route.fulfill({ status: 404, json: {} });
    });
    await page.goto('/audit/');
    await expect(page.getByRole('heading', { name: 'Event history' })).toBeVisible();
    await expect(page.getByText('Approve user', { exact: true })).toBeVisible();
    expect(await page.locator('table .mantine-Badge-label').evaluateAll(labels => labels.every(label => label.scrollWidth <= label.clientWidth))).toBe(true);
    await expect(page.getByText(resourceId, { exact: true })).toHaveCount(0);
    await page.getByRole('textbox', { name: 'Search this page' }).fill('Morgan');
    await page.getByRole('combobox', { name: 'Outcome on this page' }).selectOption('denied');
    await expect(page.getByText('Approve user', { exact: true })).toHaveCount(0);
    await expect(page.getByText('Disable user', { exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Clear page filters' }).click();
    const row = page.getByRole('row').filter({ hasText: 'Approve user' });
    await row.getByRole('button', { name: 'Show event details' }).focus();
    await page.keyboard.press('Enter');
    await expect(page.getByText(resourceId, { exact: true })).toBeVisible();
    await expect(page.getByText('membership_approved', { exact: true })).toBeVisible();
    await expect(page.getByText('do not render arbitrary metadata')).toHaveCount(0);
    await page.getByRole('button', { name: 'Filter by this action' }).click();
    await expect(page.getByRole('heading', { name: 'Event history' })).toBeFocused();
    await expect(page.getByText('Disable user', { exact: true })).toHaveCount(0);
    await page.getByRole('button', { name: 'Next page' }).click();
    await expect(page.getByText('Page 2', { exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Next page' })).toBeDisabled();
    await page.getByRole('button', { name: 'Clear history filters' }).click();
    await expect(page.getByText('Page 1', { exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Filter full history' }).click();
    await page.getByRole('textbox', { name: 'Resource ID' }).fill('invalid');
    await page.getByRole('button', { name: 'Apply filters' }).click();
    await expect(page.getByText('Enter a valid resource ID from event details.')).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    expect(writes).toEqual([]);
  });
}
