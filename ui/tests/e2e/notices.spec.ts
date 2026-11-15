import { expect, test } from '@playwright/test';

for (const width of [1280, 390]) {
  // Exercise the exported UI at desktop and phone widths, including saved receipts
  // across navigation and keyboard focus entering/leaving the publishing dialog.
  test(`application notices and history at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    const notices = [
      { id: 'maintenance', title: 'Planned maintenance', body: 'Save your work before tonight’s update. Workspaces will restart.',
        severity: 'warning', effectiveAt: '2026-11-15T10:00:00Z', readAt: null as string | null, dismissedAt: null as string | null },
      { id: 'templates', title: 'New development templates', body: 'The template catalog now includes updated development environments.',
        severity: 'info', effectiveAt: '2026-11-15T09:00:00Z', readAt: null as string | null, dismissedAt: null as string | null },
    ];
    const receipts: string[] = [];
    let published = 0;
    await page.route('**/api/v1/**', route => {
      const request = route.request();
      const url = new URL(request.url());
      if (url.pathname.endsWith('/session')) return route.fulfill({ json: {
        user: { id: 'admin', role: 'administrator', displayName: 'Demo administrator', primaryEmail: 'admin@example.test' },
        mode: 'normal', installationState: 'active', csrfToken: 'fixture',
        permissions: ['notice.list', 'notice.read', 'notice.dismiss', 'notice.publish'],
      } });
      if (url.pathname.endsWith('/admin/notices')) {
        published++;
        expect(request.headers()['x-csrf-token']).toBe('fixture');
        const input = request.postDataJSON();
        notices.push({ ...input, id: 'new', effectiveAt: '2026-11-15T10:00:00Z', readAt: null, dismissedAt: null });
        return route.fulfill({ status: 201, json: notices.at(-1) });
      }
      if (url.pathname.endsWith('/receipt')) {
        const notice = notices.find(item => url.pathname.includes(`/${item.id}/`))!;
        const { state } = request.postDataJSON();
        receipts.push(state);
        if (state === 'read') notice.readAt = '2026-11-15T11:00:00Z';
        else {
          expect(notice.readAt).not.toBeNull();
          notice.dismissedAt = '2026-11-15T11:00:00Z';
        }
        return route.fulfill({ status: 204 });
      }
      if (url.pathname.endsWith('/notices')) return route.fulfill({ json:
        url.searchParams.has('includeDismissed') ? notices : notices.filter(item => !item.dismissedAt),
      });
      return route.fulfill({ status: 404, json: {} });
    });
    await page.goto('/');
    const banner = page.getByRole('region', { name: 'Application notice' });
    await expect(banner.getByText('Planned maintenance', { exact: true })).toBeVisible();
    expect(await banner.evaluate(element => getComputedStyle(element).position)).toBe('fixed');
    expect(await banner.evaluate(element => !!element.closest('main'))).toBe(false);
    const bounds = await banner.boundingBox();
    expect(bounds!.x).toBeGreaterThanOrEqual(0);
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width);
    expect(bounds!.y).toBeGreaterThanOrEqual(72);
    await expect(page.getByText('New development templates', { exact: true })).toHaveCount(0);
    await banner.getByRole('button', { name: 'Dismiss Planned maintenance' }).focus();
    await page.keyboard.press('Enter');
    await expect(banner.getByText('New development templates', { exact: true })).toBeVisible();
    expect(receipts).toEqual(['read', 'dismissed']);
    await page.reload();
    await expect(banner.getByText('New development templates', { exact: true })).toBeVisible();
    await expect(page.getByText('Planned maintenance', { exact: true })).toHaveCount(0);
    await banner.getByRole('link', { name: 'View notices' }).click();
    await expect(page.getByRole('heading', { name: 'Notices', exact: true })).toBeVisible();
    await page.getByText('All notices', { exact: true }).click();
    await expect(page.getByRole('article', { name: 'Planned maintenance' }).getByText('Dismissed')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Dismiss Planned maintenance' })).toHaveCount(0);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    const publish = page.getByRole('button', { name: 'Publish notice', exact: true });
    await publish.click();
    const dialog = page.getByRole('dialog', { name: 'Publish notice' });
    await expect(dialog.getByRole('textbox', { name: 'Title', exact: true })).toBeFocused();
    await expect(dialog.getByRole('button', { name: 'Close publish notice' })).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();
    await expect(publish).toBeFocused();
    await publish.click();
    await dialog.getByRole('textbox', { name: 'Title', exact: true }).fill('Service update');
    await dialog.getByRole('textbox', { name: 'Message' }).fill('The service update is complete.');
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
    await dialog.getByRole('button', { name: 'Publish notice', exact: true }).click();
    await expect(dialog).toBeHidden();
    await expect(page.getByRole('article', { name: 'Service update' })).toBeVisible();
    expect(published).toBe(1);
  });
}
