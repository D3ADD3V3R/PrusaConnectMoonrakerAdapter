import { describe, expect, it } from 'vitest';
import { FilamentWorkflow } from './workflow.svelte';
import {
  type Assignment,
  type ChangeRecord,
  type ChangeService,
  type Snapshot,
  type Target,
} from './types';

const target: Target = {
  material: 'material-1',
  spool: 7,
  name: 'Orange PETG',
  type: 'PETG',
  color: '#FF8000',
};
const snapshot = (): Snapshot => ({
  connected: true,
  capabilities: ['mode', 'filament', 'color', 'request_id'].map(
    (argument) => `core.command.load_filament#${argument}`,
  ),
  state: { '/state': 'IDLE' },
});
const assigned = (record: ChangeRecord): Assignment => ({
  device_id: record.device,
  tool_id: '0',
  material_uuid: record.target.material,
  material_source: 'catalogue',
  material_name: record.target.name,
  brand_name: '',
  type: record.target.type,
  abbreviation: record.target.type,
  color_rgba: `${record.target.color}FF`,
  ...(record.target.spool === undefined
    ? {}
    : { spool_id: record.target.spool }),
  by: 'person',
  updated_at: '2026-11-19T00:00:00Z',
});

const world = () => {
  const records = new Map<string, ChangeRecord>();
  const calls: string[] = [];
  let device = snapshot();
  let current: Assignment | null = null;
  let sendFails = false;
  let writeFails = false;
  let assignmentFails = false;
  let chosen = { ...target };
  const service: ChangeService = {
    records: () =>
      Promise.resolve(
        [...records.values()].map((record) => structuredClone(record)),
      ),
    save: (record) => {
      calls.push(`save:${record.status}`);
      if (writeFails) return Promise.reject(new Error('disk full'));
      records.set(record.id, structuredClone(record));
      return Promise.resolve();
    },
    target: () => Promise.resolve({ ...chosen }),
    device: () => Promise.resolve(structuredClone(device)),
    send: (record) => {
      calls.push(`send:${record.id}`);
      if (sendFails) return Promise.reject(new Error('lost answer'));
      return Promise.resolve({ ok: true });
    },
    assignment: () => Promise.resolve(current),
    assign: (record) => {
      calls.push('assign');
      current = assigned(record);
      if (assignmentFails)
        return Promise.reject(new Error('assignment answer lost'));
      return Promise.resolve();
    },
  };
  return {
    service,
    records,
    calls,
    confirm: (id: string) => {
      device.state = {
        ...device.state,
        '/pcma/change_id': id,
        '/pcma/confirmed_change_id': id,
        '/pcma/filament_phase': 'CONFIRMED',
        '/pcma/loaded_material': target.type,
        '/pcma/loaded_color': 'C_FF8000',
      };
    },
    device: (next: Snapshot) => {
      device = next;
    },
    sendFails: () => {
      sendFails = true;
    },
    writeFails: () => {
      writeFails = true;
    },
    assignmentFails: () => {
      assignmentFails = true;
    },
    targetChanged: () => {
      chosen = { ...chosen, color: '#000000' };
    },
    replaceAssignment: (record: ChangeRecord) => {
      current = assigned({
        ...record,
        target: { ...record.target, material: 'different-material', spool: 37 },
      });
    },
  };
};

describe('filament workflow', () => {
  it('does not overwrite a Materials assignment changed independently after the request', async () => {
    const held = world();
    const workflow = new FilamentWorkflow(held.service, 'printer-1');
    await workflow.start({ material: target.material });
    const record = workflow.pending!;
    held.confirm(record.id);
    held.replaceAssignment(record);
    await workflow.reconcile();
    expect(held.calls).not.toContain('assign');
    expect(workflow.error).toBeDefined();
  });
  it('preserves an archive made while the physical command answer is delayed', async () => {
    const held = world();
    let release: () => void = () => undefined;
    const answer = new Promise<void>((resolve) => {
      release = () => {
        resolve();
      };
    });
    held.service.send = async () => {
      await answer;
      return { ok: true };
    };
    const first = new FilamentWorkflow(held.service, 'printer-1');
    const starting = first.start({ material: target.material });
    await expect.poll(() => held.records.size).toBe(1);
    const second = new FilamentWorkflow(held.service, 'printer-1');
    await second.abandon();
    release();
    await starting;
    expect([...held.records.values()][0]?.status).toBe('abandoned');
    expect(first.pending).toBeUndefined();
  });
  it('preserves an archive made while confirmation reconciliation reads the catalogue', async () => {
    const held = world();
    const first = new FilamentWorkflow(held.service, 'printer-1');
    await first.start({ material: target.material });
    held.confirm(first.pending!.id);
    let release: () => void = () => undefined;
    let reading = false;
    const gate = new Promise<void>((resolve) => {
      release = () => {
        resolve();
      };
    });
    held.service.target = async () => {
      reading = true;
      await gate;
      return { ...target };
    };
    const checking = first.reconcile();
    await expect.poll(() => reading).toBe(true);
    await new FilamentWorkflow(held.service, 'printer-1').abandon();
    release();
    await checking;
    expect([...held.records.values()][0]?.status).toBe('abandoned');
    expect(held.calls).not.toContain('assign');
  });
  it('persists before a physical command and assigns only after the matching confirmation', async () => {
    const held = world();
    const workflow = new FilamentWorkflow(held.service, 'printer-1');
    await workflow.start({ material: target.material, spool: 7 });
    expect(held.calls[0]).toBe('save:prepared');
    expect(held.calls[1]).toMatch(/^send:/u);
    await workflow.reconcile();
    expect(held.calls).not.toContain('assign');
    const id = workflow.pending?.id;
    expect(id).toBeDefined();
    held.confirm(id!);
    await workflow.reconcile();
    expect(held.calls.filter((call) => call === 'assign')).toHaveLength(1);
    expect(workflow.records[0]?.status).toBe('assigned');
  });
  it('never sends when the durable intent cannot be written', async () => {
    const held = world();
    held.writeFails();
    const workflow = new FilamentWorkflow(held.service, 'printer-1');
    await workflow.start({ material: target.material });
    expect(held.calls.some((call) => call.startsWith('send:'))).toBe(false);
    expect(workflow.error).toBeDefined();
  });
  it('recovers a lost physical acknowledgement without sending again', async () => {
    const held = world();
    held.sendFails();
    const first = new FilamentWorkflow(held.service, 'printer-1');
    await first.start({ material: target.material });
    const restored = new FilamentWorkflow(held.service, 'printer-1');
    await restored.load();
    await restored.reconcile();
    await restored.start({ material: target.material });
    expect(held.calls.filter((call) => call.startsWith('send:'))).toHaveLength(
      1,
    );
    held.confirm(restored.pending!.id);
    await restored.reconcile();
    expect(restored.records[0]?.status).toBe('assigned');
  });
  for (const mismatch of [
    'request',
    'active',
    'type',
    'color',
    'phase',
    'connected',
  ]) {
    it(`refuses an unrelated confirmation: ${mismatch}`, async () => {
      const held = world();
      const workflow = new FilamentWorkflow(held.service, 'printer-1');
      await workflow.start({ material: target.material });
      const id = workflow.pending!.id;
      const device = snapshot();
      device.state = {
        '/state': 'IDLE',
        '/pcma/change_id': id,
        '/pcma/confirmed_change_id': id,
        '/pcma/filament_phase': 'CONFIRMED',
        '/pcma/loaded_material': 'PETG',
        '/pcma/loaded_color': 'C_FF8000',
      };
      if (mismatch === 'request')
        device.state['/pcma/confirmed_change_id'] = 'old';
      if (mismatch === 'active') device.state['/pcma/change_id'] = 'newer';
      if (mismatch === 'type') device.state['/pcma/loaded_material'] = 'PLA';
      if (mismatch === 'color') device.state['/pcma/loaded_color'] = 'C_000000';
      if (mismatch === 'phase')
        device.state['/pcma/filament_phase'] = 'AWAITING_CONFIRMATION';
      if (mismatch === 'connected') device.connected = false;
      held.device(device);
      await workflow.reconcile();
      expect(held.calls).not.toContain('assign');
    });
  }
  it('does not replace an assignment with metadata edited after the request', async () => {
    const held = world();
    const workflow = new FilamentWorkflow(held.service, 'printer-1');
    await workflow.start({ material: target.material });
    held.confirm(workflow.pending!.id);
    held.targetChanged();
    await workflow.reconcile();
    expect(held.calls).not.toContain('assign');
    expect(workflow.error).toBeDefined();
  });
  it('recovers an assignment acknowledgement loss without repeating the printer command or assignment', async () => {
    const held = world();
    held.assignmentFails();
    const workflow = new FilamentWorkflow(held.service, 'printer-1');
    await workflow.start({ material: target.material });
    held.confirm(workflow.pending!.id);
    await workflow.reconcile();
    expect(workflow.pending?.status).toBe('confirmed');
    const restored = new FilamentWorkflow(held.service, 'printer-1');
    await restored.reconcile(true);
    expect(restored.records[0]?.status).toBe('assigned');
    expect(held.calls.filter((call) => call === 'assign')).toHaveLength(1);
    expect(held.calls.filter((call) => call.startsWith('send:'))).toHaveLength(
      1,
    );
  });
  it('archives only the journal and never resumes, cancels or resends', async () => {
    const held = world();
    const workflow = new FilamentWorkflow(held.service, 'printer-1');
    await workflow.start({ material: target.material });
    await workflow.abandon();
    await workflow.reconcile();
    expect(workflow.pending).toBeUndefined();
    expect(workflow.records[0]?.status).toBe('abandoned');
    expect(held.calls.filter((call) => call.startsWith('send:'))).toHaveLength(
      1,
    );
    expect(held.calls).not.toContain('assign');
  });
});
