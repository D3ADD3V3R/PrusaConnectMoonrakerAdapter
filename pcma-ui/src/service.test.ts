import { describe, expect, it } from 'vitest';
import { createService } from './service';
import { FilamentWorkflow } from './workflow.svelte';
import { capabilities, jsonResponse, namespace, testHost } from './test/host';

const materials = '/api/v1/modules/com.github.aux.prusa3d.materials';
const fixture = () => {
  const calls: {
    path: string;
    method: string;
    body: unknown;
    credentials: RequestCredentials | undefined;
    authorization: string | null;
  }[] = [];
  const journal = new Map<string, unknown>();
  const state: Record<string, unknown> = { '/state': 'IDLE' };
  let spoolTaken = false;
  let materialClass = 'FFF';
  const fetcher: typeof fetch = (input, init) => {
    let address: string;
    if (typeof input === 'string') address = input;
    else if (input instanceof URL) address = input.href;
    else address = input.url;
    const url = new URL(address);
    const method = init?.method ?? 'GET';
    const body: unknown =
      typeof init?.body === 'string' ? JSON.parse(init.body) : undefined;
    calls.push({
      path: url.pathname,
      method,
      body,
      credentials: init?.credentials,
      authorization: new Headers(init?.headers).get('Authorization'),
    });
    if (url.pathname === '/api/v1/devices/printer-1')
      return Promise.resolve(
        jsonResponse({ connected: true, capabilities, state }),
      );
    if (url.pathname === '/api/v1/devices/printer-1/commands')
      return Promise.resolve(jsonResponse({ ok: true }));
    const prefix = `/api/v1/modules/${namespace}/state`;
    if (url.pathname === prefix)
      return Promise.resolve(jsonResponse({ items: [...journal.keys()] }));
    if (url.pathname.startsWith(`${prefix}/`)) {
      const key = url.pathname.slice(prefix.length + 1);
      if (method === 'PUT') {
        const decoded =
          typeof body === 'object' && body !== null && 'value' in body
            ? body.value
            : undefined;
        journal.set(key, decoded);
      }
      return Promise.resolve(jsonResponse({ key, value: journal.get(key) }));
    }
    if (url.pathname === `${materials}/materials/pla`)
      return Promise.resolve(
        jsonResponse({
          uuid: 'pla',
          name: 'Orange PLA',
          type: 'PLA',
          class: materialClass,
          color_rgba: '#ff8000ff',
          spools: 1,
        }),
      );
    if (url.pathname === `${materials}/materials/pla/spools`)
      return Promise.resolve(
        jsonResponse({
          total: 1,
          spools: [
            {
              id: 7,
              material_uuid: 'pla',
              class: 'FFF',
              type: 'PLA',
              color_rgba: '#ff8000ff',
              archived_at: null,
              tool: spoolTaken
                ? { device_id: 'another-printer', tool_id: '0' }
                : null,
            },
          ],
        }),
      );
    if (url.pathname === `${materials}/printers/printer-1/tools`)
      return Promise.resolve(
        jsonResponse({ tools: [{ id: '0', assignment: null }] }),
      );
    if (
      url.pathname === `${materials}/printers/printer-1/tools/0` &&
      method === 'PUT'
    )
      return Promise.resolve(jsonResponse({}));
    return Promise.resolve(
      jsonResponse({ status: 404, title: 'Missing fixture route' }, 404),
    );
  };
  return {
    host: testHost(fetcher),
    calls,
    journal,
    state,
    takeSpool: () => {
      spoolTaken = true;
    },
    resin: () => {
      materialClass = 'SLA';
    },
  };
};

describe('public Materials API integration', () => {
  it('keeps Materials untouched until the matching printer confirmation, then assigns the selected spool', async () => {
    const held = fixture();
    const workflow = new FilamentWorkflow(
      createService(held.host, 'printer-1'),
      'printer-1',
    );
    await workflow.start({ material: 'pla', spool: 7 });
    expect(workflow.error).toBeUndefined();
    const id = workflow.pending!.id;
    const command = held.calls.find((call) => call.path.endsWith('/commands'));
    expect(command?.body).toEqual({
      verb: 'core.command.load_filament',
      key: id,
      args: {
        mode: 'change',
        filament: 'PLA',
        color: '#FF8000',
        request_id: id,
      },
    });
    expect(held.calls.findIndex((call) => call.method === 'PUT')).toBeLessThan(
      held.calls.findIndex((call) => call.path.endsWith('/commands')),
    );
    expect(
      held.calls.filter(
        (call) => call.path.startsWith(materials) && call.method !== 'GET',
      ),
    ).toHaveLength(0);
    held.state['/pcma/change_id'] = id;
    held.state['/pcma/confirmed_change_id'] = id;
    held.state['/pcma/filament_phase'] = 'CONFIRMED';
    held.state['/pcma/loaded_material'] = 'PLA';
    held.state['/pcma/loaded_color'] = 'C_FF8000';
    await workflow.reconcile();
    expect(workflow.error).toBeUndefined();
    expect(
      held.calls.find(
        (call) => call.path === `${materials}/printers/printer-1/tools/0`,
      )?.body,
    ).toEqual({ spool_id: 7 });
    expect(workflow.records[0]?.status).toBe('assigned');
    expect(
      held.calls.every(
        (call) =>
          call.credentials === 'same-origin' && call.authorization === null,
      ),
    ).toBe(true);
  });
  it('refuses a spool held by another printer before sending any printer command', async () => {
    const held = fixture();
    held.takeSpool();
    const workflow = new FilamentWorkflow(
      createService(held.host, 'printer-1'),
      'printer-1',
    );
    await workflow.start({ material: 'pla', spool: 7 });
    expect(workflow.error).toBeDefined();
    expect(held.calls.some((call) => call.path.endsWith('/commands'))).toBe(
      false,
    );
  });
  it('does not send a filament command for a resin material', async () => {
    const held = fixture();
    held.resin();
    const workflow = new FilamentWorkflow(
      createService(held.host, 'printer-1'),
      'printer-1',
    );
    await workflow.start({ material: 'pla' });
    expect(workflow.error).toBeDefined();
    expect(held.calls.some((call) => call.path.endsWith('/commands'))).toBe(
      false,
    );
  });
  it('recovers stored intents through the core record API and refuses corrupted records', async () => {
    const held = fixture();
    const service = createService(held.host, 'printer-1');
    const workflow = new FilamentWorkflow(service, 'printer-1');
    await workflow.start({ material: 'pla' });
    const reopened = new FilamentWorkflow(
      createService(held.host, 'printer-1'),
      'printer-1',
    );
    await reopened.load();
    await reopened.reconcile();
    expect(reopened.pending?.id).toBe(workflow.pending?.id);
    expect(
      held.calls.filter((call) => call.path.endsWith('/commands')),
    ).toHaveLength(1);
    held.journal.set('change-broken', { version: 999 });
    await reopened.start({ material: 'pla' });
    expect(reopened.error).toBeDefined();
    expect(
      held.calls.filter((call) => call.path.endsWith('/commands')),
    ).toHaveLength(1);
  });
});
