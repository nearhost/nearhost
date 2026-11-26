#!/usr/bin/env node
// Publishes the packages npm/build.mjs assembled: the platform packages
// first, so @nearhost/cli never points at a version that is not there yet.
// A package whose version is already on the registry is skipped, so a run
// that failed partway can be run again. npm takes a few minutes to show a
// version it has accepted, and refuses a second copy meanwhile (E409,
// "previously staged version"): that version is skipped too.
//
// A version with a prerelease part (0.13.0-rc.1) is published under the
// `next` tag, so it never becomes what `npm install` picks. A normal version
// then moves `next` to itself on @nearhost/cli, so `@next` is never older than
// `latest` (only the launcher: it pins its platform packages exactly). That
// runs on a rerun too, and npm allows it over trusted publishing once the
// package's trusted publisher has "Allow npm dist-tag" on (npm 11.21.0 or
// later).
//
//   node npm/publish.mjs [out dir] [--dry-run] [-- extra npm publish flags]
//
// --dry-run prints each npm publish command and touches no registry, not
// even for npm view.
import { execFileSync, spawnSync } from 'node:child_process';
import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
const split = args.indexOf('--');
const extra = split === -1 ? [] : args.slice(split + 1);
const own = split === -1 ? args : args.slice(0, split);
const dryRun = own.includes('--dry-run');
const out = own.find((a) => a !== '--dry-run') ?? join(here, 'dist');

const dirs = readdirSync(out)
  .filter((d) => d.startsWith('cli-'))
  .sort()
  .concat('cli')
  .map((d) => join(out, d));

for (const dir of dirs) {
  const { name, version } = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8'));
  const publish = [dir, '--access', 'public', ...(version.includes('-') ? ['--tag', 'next'] : []), ...extra];
  if (dryRun) {
    console.log(['npm', 'publish', ...publish].join(' '));
    continue;
  }
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
  const res = spawnSync('npm', ['publish', ...publish], { stdio: ['inherit', 'inherit', 'pipe'], encoding: 'utf8' });
  if (res.error) throw res.error;
  process.stderr.write(res.stderr);
  if (res.status === 0) continue;
  if (/\bE409\b/.test(res.stderr) && /previously staged version/i.test(res.stderr)) {
    console.log(`${name}@${version} is already with npm, which is still processing it; skipping`);
    continue;
  }
  process.exit(res.status ?? 1);
}

const launcher = JSON.parse(readFileSync(join(out, 'cli', 'package.json'), 'utf8'));
if (!launcher.version.includes('-')) {
  const tag = ['dist-tag', 'add', `${launcher.name}@${launcher.version}`, 'next'];
  if (dryRun) {
    console.log(['npm', ...tag].join(' '));
  } else {
    const res = spawnSync('npm', tag, { stdio: 'inherit' });
    if (res.error) throw res.error;
    if (res.status !== 0) process.exit(res.status ?? 1);
  }
}
