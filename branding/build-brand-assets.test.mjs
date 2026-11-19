import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { copyFile, mkdir, mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';

// Regenerating a checkout updates each asset the Next.js and Dex builds serve.
test('exports the approved runtime assets and palette into the UI', async () => {
  const repo = await mkdtemp(join(tmpdir(), 'shiftbox-brand-test-'));
  try {
    const generator = join(repo, 'branding', 'build-brand-assets.mjs');
    await mkdir(join(repo, 'branding', 'source'), { recursive: true });
    await copyFile(new URL('./build-brand-assets.mjs', import.meta.url), generator);
    await copyFile(new URL('./source/command-space.json', import.meta.url), join(repo, 'branding', 'source', 'command-space.json'));
    const require = createRequire(import.meta.url);
    execFileSync(process.execPath, [generator], {
      env: { ...process.env, SHARP_MODULE: process.env.SHARP_MODULE || require.resolve('sharp') },
    });

    const runtime = join(repo, 'ui', 'public', 'brand');
    const assets = [
      'icons/apple-touch-icon.png', 'icons/favicon.ico', 'icons/favicon.svg',
      'svg/shiftbox-logo-dark.svg', 'svg/shiftbox-logo-light.svg',
      'svg/shiftbox-mark-dark.svg', 'svg/shiftbox-mark-light.svg',
    ];
    assert.deepEqual((await readdir(runtime)).sort(), assets.map(asset => asset.split('/')[1]).sort());
    for (const asset of assets) {
      assert.deepEqual(await readFile(join(runtime, asset.split('/')[1])), await readFile(join(repo, 'branding', asset)));
    }
    assert.deepEqual(await readFile(join(repo, 'ui', 'lib', 'brand-colors.json')), await readFile(join(repo, 'branding', 'brand-colors.json')));
  } finally {
    await rm(repo, { recursive: true, force: true });
  }
});
