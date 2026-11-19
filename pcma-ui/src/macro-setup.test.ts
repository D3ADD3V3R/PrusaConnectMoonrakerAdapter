import { expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { locale } from '@prusa/connect-ui/i18n';
import Setup from './macro-setup.svelte';
import { holdHost } from './host';
import { jsonResponse, testHost } from './test/host';

it('offers an explicit install and a separate restart, with no automatic writes', async () => {
  locale.set('en');
  const writes: string[] = [];
  const plan = {
    ready: false,
    missing: ['PCMA_STATE'],
    installable: true,
    reason: '',
    disk_installed: false,
    restart_allowed: false,
    config_digest: 'a'.repeat(64),
    bundle_digest: 'b'.repeat(64),
  };
  const fetcher: typeof fetch = (input, init) => {
    let address: string;
    if (typeof input === 'string') address = input;
    else if (input instanceof URL) address = input.href;
    else address = input.url;
    const url = new URL(address);
    if (init?.method === 'POST') {
      writes.push(url.pathname);
      if (!url.pathname.endsWith('/restart'))
        Object.assign(plan, {
          installable: false,
          disk_installed: true,
          restart_allowed: true,
        });
    }
    return Promise.resolve(jsonResponse(plan));
  };
  holdHost(testHost(fetcher));
  await render(Setup, { deviceId: 'printer-1' });
  await expect
    .element(page.getByText('Missing macros: PCMA_STATE'))
    .toBeVisible();
  expect(writes).toEqual([]);
  await page
    .getByRole('button', { name: 'Install through Moonraker', exact: true })
    .click();
  expect(writes).toEqual([]);
  await page
    .getByRole('alertdialog')
    .getByRole('button', { name: 'Install through Moonraker', exact: true })
    .click();
  await expect.poll(() => writes.length).toBe(1);
  await expect
    .element(
      page.getByRole('button', { name: 'Restart Klipper now', exact: true }),
    )
    .toBeVisible();
  expect(writes.some((path) => path.endsWith('/restart'))).toBe(false);
  await page
    .getByRole('button', { name: 'Restart Klipper now', exact: true })
    .click();
  await page
    .getByRole('alertdialog')
    .getByRole('button', { name: 'Restart Klipper now', exact: true })
    .click();
  await expect.poll(() => writes.length).toBe(2);
  expect(writes[1]).toContain('/printer-1/macros/restart');
});

it('keeps custom Resume as a manual merge and offers the generic file', async () => {
  locale.set('en');
  holdHost(
    testHost(() =>
      Promise.resolve(
        jsonResponse({
          ready: false,
          missing: ['PCMA_STATE'],
          installable: false,
          reason: 'custom_resume_requires_merge',
          disk_installed: false,
          restart_allowed: false,
        }),
      ),
    ),
  );
  await render(Setup, { deviceId: 'printer-1' });
  await expect
    .element(
      page.getByText(
        'A custom RESUME macro exists. Merge the PCMA guard manually.',
      ),
    )
    .toBeVisible();
  await expect
    .element(page.getByRole('button', { name: 'Download generic macro file' }))
    .toBeVisible();
  await expect
    .element(
      page.getByRole('button', {
        name: 'Install through Moonraker',
        exact: true,
      }),
    )
    .not.toBeInTheDocument();
});
