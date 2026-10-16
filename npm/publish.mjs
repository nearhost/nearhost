#!/usr/bin/env node
// Publishes the packages npm/build.mjs assembled: the platform packages
// first, so @nearhost/cli never points at a version that is not there yet.
// A package whose version is already on the registry is skipped, so a run
// that failed partway can be run again.
//
//   node npm/publish.mjs [out dir] [-- extra npm publish flags]
import { execFileSync } from 'node:child_process';
import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
const split = args.indexOf('--');
const extra = split === -1 ? [] : args.slice(split + 1);
const out = (split === -1 ? args : args.slice(0, split))[0] ?? join(here, 'dist');

const dirs = readdirSync(out)
  .filter((d) => d.startsWith('cli-'))
  .sort()
  .concat('cli')
  .map((d) => join(out, d));

for (const dir of dirs) {
  const { name, version } = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8'));
  let published = false;
  try {
    execFileSync('npm', ['view', `${name}@${version}`, 'version'], { stdio: ['ignore', 'pipe', 'ignore'] });
    published = true;
  } catch {
    // npm view exits non-zero when the version is not there.
  }
  if (published) {
    console.log(`${name}@${version} is already published; skipping`);
    continue;
  }
  execFileSync('npm', ['publish', dir, '--access', 'public', ...extra], { stdio: 'inherit' });
}
