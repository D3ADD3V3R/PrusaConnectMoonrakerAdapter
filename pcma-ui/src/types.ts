import type { components } from 'materials-client';

export type Material = components['schemas']['Row'];
export type Spool = components['schemas']['Spool'];
export type Assignment = components['schemas']['Assignment'];
export interface Selection {
  material: string;
  spool?: number;
}
export interface Target extends Selection {
  type: string;
  color: string;
  name: string;
}
export type ChangeStatus =
  'prepared' | 'waiting' | 'uncertain' | 'confirmed' | 'assigned' | 'abandoned';
export interface ChangeRecord {
  version: 1;
  id: string;
  device: string;
  created: string;
  target: Target;
  before: AssignmentStamp | null;
  status: ChangeStatus;
  error?: string;
}
export interface AssignmentStamp {
  material: string;
  spool?: number;
  updated: string;
}
export const assignmentStamp = (
  assignment: Assignment | null,
): AssignmentStamp | null =>
  assignment === null
    ? null
    : {
        material: assignment.material_uuid,
        ...(assignment.spool_id === undefined
          ? {}
          : { spool: assignment.spool_id }),
        updated: assignment.updated_at,
      };
export interface Snapshot {
  connected: boolean;
  capabilities: readonly string[];
  state: Record<string, unknown>;
}
export interface ChangeService {
  records(): Promise<ChangeRecord[]>;
  save(record: ChangeRecord): Promise<void>;
  target(selection: Selection): Promise<Target>;
  device(): Promise<Snapshot>;
  send(record: ChangeRecord): Promise<{ ok: boolean; machine_reason?: string }>;
  assignment(): Promise<Assignment | null>;
  assign(record: ChangeRecord): Promise<void>;
}

export class WorkflowError extends Error {
  constructor(readonly key: string) {
    super(key);
    this.name = 'WorkflowError';
  }
}

export const terminal = (record: ChangeRecord): boolean =>
  record.status === 'assigned' || record.status === 'abandoned';

export const supportsChange = (device: Snapshot): boolean =>
  device.connected &&
  ['mode', 'filament', 'color', 'request_id'].every((arg) =>
    device.capabilities.includes(`core.command.load_filament#${arg}`),
  );

export const validTarget = (target: Target): boolean =>
  target.material !== '' &&
  /^[A-Z][A-Z0-9_-]{0,31}$/u.test(target.type) &&
  target.type !== 'UNKNOWN' &&
  /^#[0-9A-F]{6}$/u.test(target.color) &&
  (target.spool === undefined ||
    (Number.isSafeInteger(target.spool) && target.spool > 0));

export const confirmed = (record: ChangeRecord, device: Snapshot): boolean =>
  device.connected &&
  device.state['/pcma/change_id'] === record.id &&
  device.state['/pcma/confirmed_change_id'] === record.id &&
  device.state['/pcma/filament_phase'] === 'CONFIRMED' &&
  device.state['/pcma/loaded_material'] === record.target.type &&
  device.state['/pcma/loaded_color'] === `C_${record.target.color.slice(1)}`;

export const sameAssignment = (
  record: ChangeRecord,
  assignment: Assignment | null,
): boolean =>
  assignment !== null &&
  assignment.material_uuid === record.target.material &&
  assignment.spool_id === record.target.spool;

/** Records are untrusted JSON, including values written by another browser. */
export const changeRecord = (value: unknown): ChangeRecord | undefined => {
  if (
    typeof value !== 'object' ||
    value === null ||
    !('version' in value) ||
    value.version !== 1 ||
    !('id' in value) ||
    typeof value.id !== 'string' ||
    !/^[a-f0-9-]{36}$/u.test(value.id) ||
    !('device' in value) ||
    typeof value.device !== 'string' ||
    !('created' in value) ||
    typeof value.created !== 'string' ||
    !('status' in value) ||
    typeof value.status !== 'string' ||
    ![
      'prepared',
      'waiting',
      'uncertain',
      'confirmed',
      'assigned',
      'abandoned',
    ].includes(value.status) ||
    !('target' in value) ||
    typeof value.target !== 'object' ||
    value.target === null
  )
    return undefined;
  const target = value.target;
  if (!('before' in value)) return undefined;
  if (
    value.before !== null &&
    (typeof value.before !== 'object' ||
      !('material' in value.before) ||
      typeof value.before.material !== 'string' ||
      !('updated' in value.before) ||
      typeof value.before.updated !== 'string' ||
      ('spool' in value.before &&
        (typeof value.before.spool !== 'number' ||
          !Number.isSafeInteger(value.before.spool) ||
          value.before.spool <= 0)))
  )
    return undefined;
  if (
    !('material' in target) ||
    typeof target.material !== 'string' ||
    !('type' in target) ||
    typeof target.type !== 'string' ||
    !('color' in target) ||
    typeof target.color !== 'string' ||
    !('name' in target) ||
    typeof target.name !== 'string' ||
    ('spool' in target && typeof target.spool !== 'number') ||
    ('error' in value && typeof value.error !== 'string')
  )
    return undefined;
  // All fields are checked before narrowing to the module's schema.
  const parsed = value as ChangeRecord;
  return validTarget(parsed.target) ? parsed : undefined;
};
