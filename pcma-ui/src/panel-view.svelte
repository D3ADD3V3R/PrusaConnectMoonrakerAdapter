<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import {
    Alert,
    AlertTitle,
    Button,
    ConfirmDialog,
    Field,
    Input,
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
  } from '@prusa/connect-ui/primitives';
  import { heldHost, namespaceOf } from './host';
  import { createService, type Service } from './service';
  import { FilamentWorkflow } from './workflow.svelte';
  import {
    supportsChange,
    WorkflowError,
    type Material,
    type Spool,
  } from './types';

  let { deviceId }: { deviceId: string } = $props();
  const host = heldHost();
  const t = host.t;
  const namespace = namespaceOf(host);
  let workflow = $state.raw<FilamentWorkflow>();
  let service = $state.raw<Service>();
  let supported = $state(false);
  let ready = $state(false);
  let loading = $state(false);
  let failure = $state<unknown>(undefined);
  let materials = $state.raw<Material[]>([]);
  let spools = $state.raw<Spool[]>([]);
  let query = $state('');
  let offset = $state(0);
  let total = $state(0);
  let material = $state('');
  let spool = $state('none');
  const pending = $derived(workflow?.pending);
  const issue = $derived(workflow?.error ?? failure);
  const chosen = $derived(materials.find((item) => item.uuid === material));
  const chosenSpool = $derived(
    spools.find((item) => String(item.id) === spool),
  );
  const color = $derived(chosenSpool?.color_rgba ?? chosen?.color_rgba ?? '');
  const busy = $derived(loading || (workflow?.busy ?? false));
  const message = (error: unknown): string =>
    error instanceof WorkflowError ? t(error.key) : t('pcma.failure');

  const search = async (page = 0): Promise<void> => {
    if (service === undefined || loading) return;
    loading = true;
    failure = undefined;
    try {
      const result = await service.materials(query, page);
      materials = result.rows;
      total = result.total;
      offset = page;
      material = '';
      spool = 'none';
      spools = [];
    } catch (error) {
      failure = error;
    } finally {
      loading = false;
    }
  };

  const choose = async (uuid: string): Promise<void> => {
    material = uuid;
    spool = 'none';
    spools = [];
    failure = undefined;
    if (service === undefined || uuid === '') return;
    try {
      const result = await service.spools(uuid);
      if (material === uuid) spools = result;
    } catch (error) {
      if (material === uuid) failure = error;
    }
  };

  onMount(() => {
    const view = new AbortController();
    const id = untrack(() => deviceId);
    service = createService(host, id, view.signal);
    workflow = new FilamentWorkflow(service, id);
    const initialize = async (): Promise<void> => {
      if (service === undefined || workflow === undefined) return;
      try {
        supported = supportsChange(await service.device());
        if (supported) {
          await workflow.load();
          await search();
          await workflow.reconcile();
        }
        ready = true;
      } catch (error) {
        failure = error;
      }
    };
    void initialize();
    const timer = setInterval(() => {
      if (ready && pending !== undefined && !document.hidden)
        void workflow?.reconcile();
    }, 2000);
    return () => {
      view.abort();
      clearInterval(timer);
    };
  });
</script>

<section data-module={namespace} class="flex flex-col gap-4 p-4">
  <h2 class="text-lg font-semibold">{t('pcma.heading')}</h2>
  <p class="text-sm text-muted-foreground">{t('pcma.attribution')}</p>
  {#if issue !== undefined}
    <Alert variant="destructive"
      ><AlertTitle>{message(issue)}</AlertTitle></Alert
    >
  {/if}
  {#if !ready}
    <p>{t('pcma.loading')}</p>
  {:else if !supported}
    <p>{t('pcma.unsupported')}</p>
  {:else}
    <p class="text-sm text-muted-foreground">{t('pcma.intro')}</p>
    {#if pending !== undefined}
      <div
        data-part="pending-request"
        class="flex flex-col gap-2 rounded-md border border-border p-4"
      >
        <h3 class="font-semibold">{t('pcma.pending')}</h3>
        <p>
          {pending.target.name} · {pending.target.type} · {pending.target.color}
        </p>
        {#if pending.target.spool !== undefined}<p>
            {t('pcma.spoolNumber', { id: pending.target.spool })}
          </p>{/if}
        <p>{t(`pcma.status.${pending.status}`)}</p>
        <p class="font-mono text-xs">{pending.id}</p>
        {#if pending.error !== undefined}<p class="text-sm text-destructive">
            {pending.error.startsWith('pcma.')
              ? t(pending.error)
              : pending.error}
          </p>{/if}
        <p class="text-sm text-muted-foreground">
          {t('pcma.confirmAtPrinter')}
        </p>
        <div class="flex flex-wrap gap-2">
          <Button
            class="h-auto min-h-9 max-w-full whitespace-normal"
            disabled={busy}
            onclick={() => void workflow?.reconcile(true)}
            >{t('pcma.refresh')}</Button
          >
          <ConfirmDialog
            trigger={archiveTrigger}
            title={t('pcma.archive')}
            description={t('pcma.archiveHint')}
            confirmLabel={t('pcma.archive')}
            onConfirm={() => void workflow?.abandon()}
          />
        </div>
      </div>
    {:else}
      {#if workflow?.records.some((record) => record.status === 'assigned')}
        <p class="text-sm">{t('pcma.assigned')}</p>
      {/if}
      <div class="flex flex-wrap items-end gap-2">
        <Field
          label={t('pcma.search')}
          control={Input}
          controlProps={{
            value: query,
            oninput: (event: Event) => {
              if (event.currentTarget instanceof HTMLInputElement)
                query = event.currentTarget.value;
            },
          }}
        />
        <Button disabled={busy} onclick={() => void search()}
          >{t('pcma.searchButton')}</Button
        >
      </div>
      <Select
        value={material}
        onValueChange={(value: string) => void choose(value)}
        items={materials.map((item) => ({
          value: item.uuid,
          label: `${item.name} · ${item.type} · ${item.color_rgba}`,
        }))}
      >
        <SelectTrigger aria-label={t('pcma.material')}
          ><SelectValue placeholder={t('pcma.chooseMaterial')} /></SelectTrigger
        >
        <SelectContent
          >{#each materials as item (item.uuid)}<SelectItem
              value={item.uuid}
              label={`${item.name} · ${item.type} · ${item.color_rgba}`}
              >{item.name} · {item.type} · {item.color_rgba}</SelectItem
            >{/each}</SelectContent
        >
      </Select>
      <div class="flex flex-wrap items-center gap-2">
        <Button
          variant="secondary"
          disabled={busy || offset === 0}
          onclick={() => void search(Math.max(0, offset - 25))}
          >{t('pcma.previous')}</Button
        >
        <span class="text-sm">{t('pcma.count', { count: total })}</span>
        <Button
          variant="secondary"
          disabled={busy || offset + 25 >= total}
          onclick={() => void search(offset + 25)}>{t('pcma.next')}</Button
        >
      </div>
      {#if material !== ''}
        <Select
          value={spool}
          onValueChange={(value: string) => {
            spool = value;
          }}
          items={[
            { value: 'none', label: t('pcma.noSpool') },
            ...spools.map((item) => ({
              value: String(item.id),
              label: t('pcma.spoolNumber', { id: item.id }),
            })),
          ]}
        >
          <SelectTrigger aria-label={t('pcma.spool')}
            ><SelectValue /></SelectTrigger
          >
          <SelectContent>
            <SelectItem value="none" label={t('pcma.noSpool')}
              >{t('pcma.noSpool')}</SelectItem
            >
            {#each spools as item (item.id)}
              <SelectItem
                value={String(item.id)}
                label={t('pcma.spoolNumber', { id: item.id })}
                disabled={item.archived_at !== null ||
                  (item.tool !== null &&
                    (item.tool.device_id !== deviceId ||
                      item.tool.tool_id !== '0'))}
              >
                {t('pcma.spoolNumber', { id: item.id })} · {item.remaining_weight_g}
                g
              </SelectItem>
            {/each}
          </SelectContent>
        </Select>
        <p>
          {t('pcma.selected', {
            material: chosenSpool?.type ?? chosen?.type ?? '',
            color,
          })}
        </p>
      {/if}
      <Button
        disabled={busy || material === '' || !(workflow?.loaded ?? false)}
        onclick={() =>
          void workflow?.start({
            material,
            ...(spool === 'none' ? {} : { spool: Number(spool) }),
          })}>{t('pcma.start')}</Button
      >
    {/if}
  {/if}
</section>

{#snippet archiveTrigger({ props }: { props: Record<string, unknown> })}
  <Button {...props} variant="secondary" disabled={busy}
    >{t('pcma.archive')}</Button
  >
{/snippet}
