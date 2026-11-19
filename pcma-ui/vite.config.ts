import { copyFileSync, cpSync, mkdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { moduleBuild, moduleDev } from '@prusa/connect-ui/build';

const bundle = moduleBuild({ entry: 'src/index.ts', outDir: 'dist/ui' });
export default ({ command }: { command: 'build' | 'serve' }) =>
  command === 'serve'
    ? moduleDev()
    : {
        ...bundle,
        plugins: [
          ...(bundle.plugins ?? []),
          {
            name: 'pcma:manifest',
            closeBundle() {
              const root = fileURLToPath(new URL('./dist/', import.meta.url));
              mkdirSync(root, { recursive: true });
              copyFileSync('module.yaml', `${root}module.yaml`);
              copyFileSync('../LICENCE.md', `${root}LICENCE.md`);
              copyFileSync('../NOTICE.md', `${root}NOTICE.md`);
              cpSync('src/locales', `${root}ui/locales`, { recursive: true });
            },
          },
        ],
      };
