<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { Button, ConfirmDialog } from '@prusa/connect-ui/primitives';
  import { heldHost } from './host';
  import { macroSetup, type MacroStatus } from './macro-setup';
  import config from '../../adapter/macroconfig/pcma-filament.cfg?raw';

  let {
    deviceId,
    onStatus,
  }: {
    deviceId: string;
    onStatus?: (status: MacroStatus | undefined) => void;
  } = $props();
  const host = heldHost();
  const t = host.t;
  let api: ReturnType<typeof macroSetup> | undefined;
  let status = $state.raw<MacroStatus>();
  let busy = $state(false);
  let failed = $state(false);
  let restarted = $state(false);

  const refresh = async () => {
    if (api === undefined || busy) return;
    busy = true;
    failed = false;
    try {
      status = await api.status();
      onStatus?.(status);
    } catch {
      failed = true;
      status = undefined;
      onStatus?.(undefined);
    } finally {
      busy = false;
    }
  };
  const install = async () => {
    if (api === undefined || status === undefined || busy) return;
    busy = true;
    failed = false;
    try {
      status = await api.install(status);
      onStatus?.(status);
    } catch {
      failed = true;
      status = undefined;
      onStatus?.(undefined);
    } finally {
      busy = false;
    }
  };
  const restart = async () => {
    if (api === undefined || status === undefined || busy) return;
    busy = true;
    failed = false;
    try {
      await api.restart(status);
      restarted = true;
      status = undefined;
    } catch {
      failed = true;
      status = undefined;
    } finally {
      busy = false;
    }
  };
  const download = () => {
    const url = URL.createObjectURL(
      new Blob([config], { type: 'text/plain;charset=utf-8' }),
    );
    const link = document.createElement('a');
    link.href = url;
    link.download = 'pcma-filament.cfg';
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
  onMount(() => {
    const local = new AbortController();
    api = macroSetup(
      host,
      untrack(() => deviceId),
      AbortSignal.any([local.signal, host.signal]),
    );
    void refresh();
    return () => local.abort();
  });
</script>

<section
  class="flex flex-col gap-2 rounded-md border border-border p-4"
  aria-label={t('pcma.macros.heading')}
>
  <h3 class="font-semibold">{t('pcma.macros.heading')}</h3>
  {#if status?.ready}
    <p>{t('pcma.macros.ready')}</p>
  {:else if status !== undefined}
    <p>{t('pcma.macros.missing')}: {status.missing.join(', ')}</p>
    {#if status.reason !== ''}<p>
        {t(`pcma.macros.reason.${status.reason}`)}
      </p>{/if}
    {#if status.disk_installed}<p>{t('pcma.macros.restartNeeded')}</p>{/if}
  {:else if busy}
    <p>{t('pcma.macros.checking')}</p>
  {/if}
  {#if failed}<p class="text-destructive">{t('pcma.macros.failure')}</p>{/if}
  {#if restarted}<p>{t('pcma.macros.restarted')}</p>{/if}
  {#if status?.backup}<p>{t('pcma.macros.backup')}: {status.backup}</p>{/if}
  <div class="flex flex-wrap gap-2">
    <Button disabled={busy} onclick={() => void refresh()}
      >{t('pcma.macros.refresh')}</Button
    >
    <Button onclick={download}>{t('pcma.macros.download')}</Button>
    {#if status?.installable}
      <ConfirmDialog
        variant="confirm"
        trigger={installTrigger}
        title={t('pcma.macros.install')}
        description={t('pcma.macros.installHint')}
        confirmLabel={t('pcma.macros.install')}
        onConfirm={() => void install()}
      />
    {/if}
    {#if status?.restart_allowed && !status.ready}
      <ConfirmDialog
        variant="confirm"
        trigger={restartTrigger}
        title={t('pcma.macros.restart')}
        description={t('pcma.macros.restartHint')}
        confirmLabel={t('pcma.macros.restart')}
        onConfirm={() => void restart()}
      />
    {/if}
  </div>
</section>

{#snippet installTrigger({ props }: { props: Record<string, unknown> })}<Button
    {...props}
    disabled={busy}>{t('pcma.macros.install')}</Button
  >{/snippet}
{#snippet restartTrigger({ props }: { props: Record<string, unknown> })}<Button
    {...props}
    disabled={busy}>{t('pcma.macros.restart')}</Button
  >{/snippet}
