#!/usr/bin/env node
// Publishes the packages npm/build.mjs assembled: the platform packages
// first, so @nearhost/cli never points at a version that is not there yet,
// then @nearhost/cli, then @nearhost/api (which has no platform packages).
// A package whose version is already on the registry is skipped, so a run
// that failed partway can be run again. npm takes a few minutes to show a
// version it has accepted, and refuses a second copy meanwhile (E409,
// "previously staged version"): that version is skipped too.
//
// A version with a prerelease part (0.16.0-beta.1) is published under the
// `prerelease` tag, so it never becomes what `npm install` picks. Then, for
// any version, the `next` tag of @nearhost/cli and of @nearhost/api moves to
// the highest of itself, `latest` and this version, so `next` only ever moves
// forward: release runs go in parallel and finish in any order, and a beta
// that finishes after its release, or a rerun of an old release, must not
// move it back. Of the CLI packages only the launcher is tagged: it pins its
// platform packages exactly. npm allows dist-tag over trusted publishing once
// the package's trusted publisher has "Allow npm dist-tag" on (npm 11.21.0 or
// later).
//
// @nearhost/api is published last and does not hold the CLI back: if its
// publish fails, @nearhost/cli's `next` still moves, @nearhost/api's does not,
// and the run then exits with npm's status.
//
//   node npm/publish.mjs [out dir] [--dry-run] [-- extra npm publish flags]
//
// --dry-run prints each npm publish command, and the dist-tag command as if
// this version were the highest, and touches no registry, not even for npm
// view.
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
  .concat('cli', 'api')
  .map((d) => join(out, d));

let apiFailed = 0;
for (const dir of dirs) {
  const { name, version } = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8'));
  const publish = [dir, '--access', 'public', ...(version.includes('-') ? ['--tag', 'prerelease'] : []), ...extra];
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
  if (dir !== join(out, 'api')) process.exit(res.status ?? 1);
  apiFailed = res.status ?? 1;
}

// Compares two versions by SemVer precedence: below 0, 0 or above 0.
function compare(a, b) {
  const [ac, ap] = parse(a);
  const [bc, bp] = parse(b);
  for (let i = 0; i < 3; i++) if (ac[i] !== bc[i]) return ac[i] - bc[i];
  if (!ap.length || !bp.length) return bp.length - ap.length;
  for (let i = 0; i < Math.min(ap.length, bp.length); i++) {
    const [x, y] = [ap[i], bp[i]];
    if (x === y) continue;
    const [xn, yn] = [/^\d+$/.test(x), /^\d+$/.test(y)];
    if (xn && yn) return Number(x) - Number(y);
    if (xn !== yn) return xn ? -1 : 1;
    return x < y ? -1 : 1;
  }
  return ap.length - bp.length;
}

function parse(v) {
  const [core, ...pre] = v.split('+')[0].split('-');
  return [core.split('.').map(Number), pre.length ? pre.join('-').split('.') : []];
}

for (const dir of apiFailed ? ['cli'] : ['cli', 'api']) {
  const pkg = JSON.parse(readFileSync(join(out, dir, 'package.json'), 'utf8'));
  moveNext(pkg.name, pkg.version);
}

function moveNext(name, version) {
  if (dryRun) {
    console.log(['npm', 'dist-tag', 'add', `${name}@${version}`, 'next'].join(' '));
    return;
  }
  const tags = JSON.parse(
    execFileSync('npm', ['view', name, 'dist-tags', '--json'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'inherit'] }),
  );
  const target = [version, tags.latest, tags.next].filter(Boolean).reduce((a, b) => (compare(a, b) >= 0 ? a : b));
  if (target === tags.next) {
    console.log(`${name}'s next tag stays at ${tags.next}`);
    return;
  }
  const res = spawnSync('npm', ['dist-tag', 'add', `${name}@${target}`, 'next'], { stdio: 'inherit' });
  if (res.error) throw res.error;
  if (res.status !== 0) process.exit(res.status ?? 1);
}
if (apiFailed) process.exit(apiFailed);
