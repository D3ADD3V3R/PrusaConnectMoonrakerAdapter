import { bind } from 'materials-client';
import type { ModuleHost } from '@prusa/connect-ui/slots';
import { namespaceOf } from './host';
import {
  changeRecord,
  validTarget,
  WorkflowError,
  type ChangeRecord,
  type ChangeService,
  type Material,
  type Selection,
  type Spool,
  type Target,
} from './types';

export const createService = (
  host: ModuleHost,
  deviceId: string,
  viewSignal: AbortSignal = host.signal,
) => {
  const materials = bind(host.client);
  const namespace = namespaceOf(host);
  const signal = AbortSignal.any([host.signal, viewSignal]);
  const path = (record: ChangeRecord) => ({
    namespace,
    key: `change-${record.id}`,
  });

  const spools = async (uuid: string): Promise<Spool[]> => {
    const result: Spool[] = [];
    for (let offset = 0; offset < 10000; offset += 100) {
      const page = await materials.listMaterialSpools(
        { path: { uuid }, query: { offset, limit: 100 } },
        signal,
      );
      result.push(...page.spools);
      if (result.length >= page.total || page.spools.length === 0)
        return result;
    }
    throw new WorkflowError('pcma.tooManySpools');
  };

  const target = async (selection: Selection): Promise<Target> => {
    const detail = await materials.getMaterial(
      { path: { uuid: selection.material } },
      signal,
    );
    let source: Material | Spool = detail;
    if (selection.spool !== undefined) {
      const spool = (await spools(selection.material)).find(
        (item) => item.id === selection.spool,
      );
      if (
        spool?.archived_at !== null ||
        spool.material_uuid !== selection.material ||
        (spool.tool !== null &&
          (spool.tool.device_id !== deviceId || spool.tool.tool_id !== '0'))
      )
        throw new WorkflowError('pcma.spoolUnavailable');
      source = spool;
    }
    if (
      source.class !== 'FFF' ||
      !/^#[a-f0-9]{6}(?:[a-f0-9]{2})?$/iu.test(source.color_rgba)
    )
      throw new WorkflowError('pcma.invalidMetadata');
    const result: Target = {
      ...selection,
      type: source.type.toUpperCase(),
      color: source.color_rgba.slice(0, 7).toUpperCase(),
      name: detail.name,
    };
    if (!validTarget(result)) throw new WorkflowError('pcma.invalidMetadata');
    return result;
  };

  const service: ChangeService = {
    records: async () => {
      const listing = await host.client.listModuleState(
        { path: { namespace } },
        signal,
      );
      const records: ChangeRecord[] = [];
      for (const key of (listing.items ?? []).filter((item) =>
        item.startsWith('change-'),
      )) {
        const row = await host.client.getModuleState(
          { path: { namespace, key } },
          signal,
        );
        const record = changeRecord(row.value);
        if (record === undefined || key !== `change-${record.id}`)
          throw new WorkflowError('pcma.invalidJournal');
        if (record.device === deviceId) records.push(record);
      }
      return records.sort((a, b) => a.created.localeCompare(b.created));
    },
    save: async (record) => {
      await host.client.putModuleState(
        { path: path(record), body: { value: record } },
        signal,
      );
    },
    target,
    device: async () => {
      const device = await host.client.getDevice(
        { path: { device_id: deviceId } },
        signal,
      );
      return {
        connected: device.connected,
        capabilities: device.capabilities ?? [],
        state: device.state,
      };
    },
    send: async (record) =>
      await host.client.sendDeviceCommand(
        {
          path: { device_id: deviceId },
          body: {
            verb: 'core.command.load_filament',
            key: record.id,
            args: {
              mode: 'change',
              filament: record.target.type,
              color: record.target.color,
              request_id: record.id,
            },
          },
        },
        signal,
      ),
    assignment: async () =>
      (
        await materials.listTools({ path: { device: deviceId } }, signal)
      ).tools.find((tool) => tool.id === '0')?.assignment ?? null,
    assign: async (record) => {
      await materials.assignTool(
        {
          path: { device: deviceId, tool: '0' },
          body:
            record.target.spool === undefined
              ? { material_uuid: record.target.material }
              : { spool_id: record.target.spool },
        },
        signal,
      );
    },
  };
  return {
    ...service,
    materials: async (q: string, offset: number) =>
      await materials.listMaterials(
        { query: { q, class: 'FFF', offset, limit: 25 } },
        signal,
      ),
    spools,
  };
};
export type Service = ReturnType<typeof createService>;
