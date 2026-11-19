import { expect, type Locator } from '@playwright/test';

export async function expectReadableText(locator: Locator) {
  await expect(locator).toBeVisible();
  const ratio = await locator.evaluate(element => {
    function luminance(color: string) {
      const channels = color.match(/^rgb\((\d+), (\d+), (\d+)\)$/)?.slice(1).map(Number);
      if (!channels) throw new Error(`Expected an opaque RGB color, received ${color}`);
      const [red, green, blue] = channels.map(channel => {
        const value = channel / 255;
        return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
      });
      return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
    }

    for (let surface: Element | null = element; surface; surface = surface.parentElement) {
      const background = getComputedStyle(surface).backgroundColor;
      if (background === 'rgba(0, 0, 0, 0)') continue;
      const text = luminance(getComputedStyle(element).color);
      const fill = luminance(background);
      return (Math.max(text, fill) + 0.05) / (Math.min(text, fill) + 0.05);
    }
    throw new Error('The text has no opaque background');
  });
  expect(ratio, 'WCAG AA contrast for normal text').toBeGreaterThanOrEqual(4.5);
}
