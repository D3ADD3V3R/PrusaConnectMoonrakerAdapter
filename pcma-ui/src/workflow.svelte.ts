import {
  assignmentStamp,
  confirmed,
  sameAssignment,
  supportsChange,
  terminal,
  WorkflowError,
  type ChangeRecord,
  type ChangeService,
  type Selection,
} from './types';

/** A durable intent is written before sending a physical command, never replayed. */
export class FilamentWorkflow {
  records = $state.raw<ChangeRecord[]>([]);
  busy = $state(false);
  error = $state<unknown>(undefined);
  loaded = $state(false);
  readonly #service: ChangeService;
  readonly #device: string;

  constructor(service: ChangeService, device: string) {
    this.#service = service;
    this.#device = device;
  }
  get pending(): ChangeRecord | undefined {
    return this.records.find((record) => !terminal(record));
  }

  async load(): Promise<void> {
    this.records = await this.#service.records();
    this.loaded = true;
  }

  async #save(record: ChangeRecord): Promise<void> {
    await this.#service.save(record);
    this.records = [
      ...this.records.filter((item) => item.id !== record.id),
      record,
    ].sort((a, b) => a.created.localeCompare(b.created));
  }

  async #stillPending(record: ChangeRecord): Promise<boolean> {
    await this.load();
    const current = this.records.find((item) => item.id === record.id);
    return current !== undefined && !terminal(current);
  }

  async start(selection: Selection): Promise<void> {
    if (this.busy) return;
    this.busy = true;
    this.error = undefined;
    try {
      await this.load();
      if (this.pending !== undefined)
        throw new WorkflowError('pcma.alreadyPending');
      const target = await this.#service.target(selection);
      const before = assignmentStamp(await this.#service.assignment());
      const device = await this.#service.device();
      if (
        !supportsChange(device) ||
        !['IDLE', 'FINISHED', 'STOPPED', 'PAUSED'].includes(
          String(device.state['/state']),
        )
      )
        throw new WorkflowError('pcma.notReady');
      const record: ChangeRecord = {
        version: 1,
        // LAN installations commonly use HTTP, where randomUUID is unavailable.
        id: Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
          byte.toString(16).padStart(2, '0'),
        ).join(''),
        device: this.#device,
        target,
        before,
        created: String(Date.now()),
        status: 'prepared',
      };
      await this.#save(record);
      try {
        const result = await this.#service.send(record);
        if (!(await this.#stillPending(record))) return;
        await this.#save({
          ...record,
          status: result.ok ? 'waiting' : 'uncertain',
          ...(result.ok
            ? {}
            : { error: result.machine_reason ?? 'pcma.commandRefused' }),
        });
      } catch (error) {
        this.error = error;
        if (!(await this.#stillPending(record))) return;
        await this.#save({
          ...record,
          status: 'uncertain',
          error: 'pcma.uncertain',
        });
      }
    } catch (error) {
      this.error = error;
    } finally {
      this.busy = false;
    }
  }

  async reconcile(retry = false): Promise<void> {
    if (this.busy) return;
    this.busy = true;
    try {
      await this.load();
      const record = this.pending;
      if (
        record === undefined ||
        (record.status === 'confirmed' && record.error !== undefined && !retry)
      )
        return;
      if (!confirmed(record, await this.#service.device())) return;
      const target = await this.#service.target(record.target);
      if (
        target.type !== record.target.type ||
        target.color !== record.target.color
      )
        throw new WorkflowError('pcma.targetChanged');
      if (!(await this.#stillPending(record))) return;
      const ready: ChangeRecord = { ...record, status: 'confirmed' };
      delete ready.error;
      await this.#save(ready);
      try {
        const currentAssignment = await this.#service.assignment();
        if (!sameAssignment(ready, currentAssignment)) {
          const beforeIdentity = ready.before;
          const unchangedIdentity =
            (currentAssignment === null && beforeIdentity === null) ||
            (currentAssignment !== null &&
              beforeIdentity !== null &&
              currentAssignment.material_uuid === beforeIdentity.material &&
              currentAssignment.spool_id === beforeIdentity.spool);
          const reportedConfirmation =
            currentAssignment?.by === 'printer' &&
            (
              currentAssignment.type || currentAssignment.abbreviation
            ).toUpperCase() === ready.target.type &&
            currentAssignment.color_rgba.slice(0, 7).toUpperCase() ===
              ready.target.color;
          if (!unchangedIdentity && !reportedConfirmation)
            throw new WorkflowError('pcma.assignmentChanged');
          // Recheck the request after the catalogue reads; an old confirmation is not a new one.
          if (!confirmed(ready, await this.#service.device())) return;
          if (!(await this.#stillPending(ready))) return;
          await this.#service.assign(ready);
        }
        if (!(await this.#stillPending(ready))) return;
        await this.#save({ ...ready, status: 'assigned' });
        this.error = undefined;
      } catch (error) {
        this.error = error;
        if (!(await this.#stillPending(ready))) return;
        await this.#save({ ...ready, error: 'pcma.assignmentFailed' });
      }
    } catch (error) {
      this.error = error;
    } finally {
      this.busy = false;
    }
  }

  /** Archives metadata only. It never cancels, resumes or repeats a printer action. */
  async abandon(): Promise<void> {
    if (this.busy) return;
    this.busy = true;
    try {
      await this.load();
      if (this.pending !== undefined)
        await this.#save({ ...this.pending, status: 'abandoned' });
    } catch (error) {
      this.error = error;
    } finally {
      this.busy = false;
    }
  }
}
