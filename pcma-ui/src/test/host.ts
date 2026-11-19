import { PrusaConnect } from '@prusa/connect';
import { registerCatalogue, translateFor } from '@prusa/connect-ui/i18n';
import {
  buildRegister,
  noComponents,
  type ModuleHost,
} from '@prusa/connect-ui/slots';
import english from '../locales/en.json';
import german from '../locales/de.json';

export const namespace = 'local.pcma.aux.tools';
export const capabilities = [
  'core.command.load_filament',
  ...['mode', 'filament', 'color', 'request_id'].map(
    (key) => `core.command.load_filament#${key}`,
  ),
];

export const testHost = (fetcher: typeof fetch): ModuleHost => {
  registerCatalogue(namespace, 'en', english);
  registerCatalogue(namespace, 'de', german);
  return {
    client: new PrusaConnect({
      url: 'http://pcma.test',
      auth: { kind: 'session' },
      fetch: fetcher,
      retries: 0,
      retryDelay: 0,
    }),
    principal: { id: 'user:operator', grants: [] },
    register: buildRegister({
      page: () => undefined,
      section: () => undefined,
      action: () => undefined,
    }),
    components: noComponents,
    store: {
      get: () => undefined,
      set: () => undefined,
      delete: () => undefined,
      keys: () => [],
    },
    fleet: {
      devices: [],
      device: () => undefined,
      loading: false,
      loaded: true,
      error: undefined,
    },
    t: translateFor(namespace),
    signal: new AbortController().signal,
    pageHref: (path) => `/m/${namespace}/${path}`,
  };
};

export const jsonResponse = (value: unknown, status = 200): Response =>
  new Response(JSON.stringify(value), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
