import type { ModuleHost } from '@prusa/connect-ui/slots';

let current: ModuleHost | undefined;
export const holdHost = (host: ModuleHost): void => {
  if (host.signal.aborted) return;
  current = host;
  host.signal.addEventListener(
    'abort',
    () => {
      if (current === host) current = undefined;
    },
    { once: true },
  );
};
export const heldHost = (): ModuleHost => {
  if (current === undefined) throw new Error('PCMA module is not active');
  return current;
};
export const namespaceOf = (host: ModuleHost): string => {
  const namespace = decodeURIComponent(
    host.pageHref('pcma').split('/')[2] ?? '',
  );
  if (!/^[a-z0-9.-]+$/u.test(namespace))
    throw new Error('PCMA namespace is missing');
  return namespace;
};
