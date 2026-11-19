<script lang="ts">
  import PanelView from './panel-view.svelte';
  let { deviceId }: { deviceId: string } = $props();
</script>

{#key deviceId}<PanelView {deviceId} />{/key}
