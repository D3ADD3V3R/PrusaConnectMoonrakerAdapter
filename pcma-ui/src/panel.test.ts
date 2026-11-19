import { expect, it } from 'vitest';
import { page } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import { locale } from '@prusa/connect-ui/i18n';
import Panel from './panel.svelte';
import { holdHost } from './host';
import { capabilities, jsonResponse, namespace, testHost } from './test/host';

it('uses the newly selected printer after navigating and includes material, color and request ID', async () => {
  const sent: string[] = [];
  const journal = new Map<string, unknown>();
  const row = {
    uuid: 'pla',
    name: 'Orange PLA',
    type: 'PLA',
    class: 'FFF',
    color_rgba: '#ff8000ff',
    spools: 0,
  };
  const fetcher: typeof fetch = (input, init) => {
    let address: string;
    if (typeof input === 'string') address = input;
    else address = input instanceof URL ? input.href : input.url;
    const path = new URL(address).pathname;
    const method = init?.method ?? 'GET';
    if (path.endsWith('/commands')) {
      sent.push(path);
      return Promise.resolve(jsonResponse({ ok: true }));
    }
    if (path.startsWith('/api/v1/devices/'))
      return Promise.resolve(
        jsonResponse({
          connected: true,
          capabilities,
          state: { '/state': 'IDLE' },
        }),
      );
    const statePrefix = `/api/v1/modules/${namespace}/state`;
    if (path === statePrefix)
      return Promise.resolve(jsonResponse({ items: [...journal.keys()] }));
    if (path.startsWith(statePrefix + '/')) {
      const key = path.slice(statePrefix.length + 1);
      if (method === 'PUT' && typeof init?.body === 'string') {
        const body: unknown = JSON.parse(init.body);
        if (typeof body === 'object' && body !== null && 'value' in body)
          journal.set(key, body.value);
      }
      return Promise.resolve(jsonResponse({ key, value: journal.get(key) }));
    }
    if (path.endsWith('/materials'))
      return Promise.resolve(jsonResponse({ rows: [row], total: 1 }));
    if (path.endsWith('/materials/pla'))
      return Promise.resolve(jsonResponse(row));
    if (path.endsWith('/spools'))
      return Promise.resolve(jsonResponse({ spools: [], total: 0 }));
    if (path.endsWith('/tools'))
      return Promise.resolve(
        jsonResponse({ tools: [{ id: '0', assignment: null }] }),
      );
    return Promise.resolve(jsonResponse({}, 404));
  };
  holdHost(testHost(fetcher));
  const view = await render(Panel, { deviceId: 'old' });
  await expect
    .element(page.getByLabelText('Material', { exact: true }))
    .toBeVisible();
  await view.rerender({ deviceId: 'new' });
  await expect
    .element(page.getByLabelText('Material', { exact: true }))
    .toBeVisible();
  await page.getByLabelText('Material', { exact: true }).click();
  await page.getByRole('option', { name: /Orange PLA/u }).click();
  await expect
    .element(page.getByText('Selected: PLA, #ff8000ff'))
    .toBeVisible();
  await page
    .getByRole('button', { name: 'Request filament change', exact: true })
    .click();
  await expect.poll(() => sent).toEqual(['/api/v1/devices/new/commands']);
  await expect.element(page.getByText('Pending filament change')).toBeVisible();
  await expect
    .element(page.getByText('Orange PLA · PLA · #FF8000'))
    .toBeVisible();
  if (import.meta.env.VITE_PCMA_CAPTURE === '1') {
    await page.viewport(1100, 800);
    await page.screenshot({
      path: '../.vitest/pcma-filament-desktop.png',
      fullPage: true,
    });
    await page.viewport(390, 844);
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390);
    await page.screenshot({
      path: '../.vitest/pcma-filament-mobile.png',
      fullPage: true,
    });
    locale.set('de');
    await expect
      .element(
        page.getByRole('button', {
          name: 'Bestätigung / Zuordnung prüfen',
        }),
      )
      .toBeVisible();
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390);
    const card = document.querySelector('[data-part="pending-request"]');
    const button = page
      .getByRole('button', { name: 'Bestätigung / Zuordnung prüfen' })
      .element();
    expect(card).not.toBeNull();
    expect(button.getBoundingClientRect().right).toBeLessThanOrEqual(
      card!.getBoundingClientRect().right,
    );
    await page.screenshot({
      path: '../.vitest/pcma-filament-mobile-de.png',
      fullPage: true,
    });
    locale.set('en');
  }
});
