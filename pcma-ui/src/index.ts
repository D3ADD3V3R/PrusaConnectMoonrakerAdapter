import { defineModule } from '@prusa/connect-ui/slots';
import FilamentPanel from './panel.svelte';
import { holdHost } from './host';
import './styles.css';

export default defineModule((host) => {
  holdHost(host);
  host.register.deviceTab('filament', FilamentPanel);
});
