import type { ModuleHost } from '@prusa/connect-ui/slots';
import type { Operation } from '@prusa/connect';

export interface MacroStatus {
  missing: string[];
  ready: boolean;
  installable: boolean;
  reason: string;
  disk_installed: boolean;
  restart_allowed: boolean;
  config_digest: string;
  bundle_digest: string;
  backup?: string;
}
const prefix =
  '/api/v1/modules/com.github.aux.d3add3v3r.prusaconnectmoonrakeradapter.setup';
export const macroSetup = (
  host: ModuleHost,
  device: string,
  signal: AbortSignal,
) => {
  const call = <T>(method: 'GET' | 'POST', suffix = '', body?: unknown) => {
    const operation: Operation = {
      id: `pcma-macros-${method}${suffix}`,
      method,
      path: `${prefix}/devices/{id}/macros${suffix}`,
      response: 'json',
      anonymous: false,
      longRunning: true,
      ...(method === 'POST' ? { body: 'application/json' } : {}),
    };
    return host.client.transport.call<T>(
      operation,
      { path: { id: device }, body },
      signal,
    );
  };
  return {
    status: () => call<MacroStatus>('GET'),
    install: (plan: MacroStatus) =>
      call<MacroStatus>('POST', '', {
        config_digest: plan.config_digest,
        bundle_digest: plan.bundle_digest,
      }),
    restart: (plan: MacroStatus) =>
      call<{ restart_requested: boolean }>('POST', '/restart', {
        config_digest: plan.config_digest,
      }),
  };
};
