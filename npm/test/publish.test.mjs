import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { chmodSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const publish = join(dirname(fileURLToPath(import.meta.url)), '..', 'publish.mjs');

// Lists the npm publish commands publish.mjs would run for these packages.
function dryRun(version, extra = []) {
  const out = mkdtempSync(join(tmpdir(), 'nearhost-publish-test-'));
  try {
    for (const name of ['cli-linux-x64', 'cli', 'api']) {
      mkdirSync(join(out, name));
      writeFileSync(join(out, name, 'package.json'), JSON.stringify({ name: `@nearhost/${name}`, version }));
    }
    return execFileSync(process.execPath, [publish, out, '--dry-run', ...extra], { encoding: 'utf8' })
      .trim()
      .split('\n');
  } finally {
    rmSync(out, { recursive: true, force: true });
  }
}

test('a normal version publishes with no --tag, then moves next to it', () => {
  const lines = dryRun('0.13.0');
  assert.equal(lines.length, 5);
  for (const l of lines.slice(0, 3)) {
    assert.match(l, /^npm publish .* --access public/);
    assert.ok(!l.includes('--tag'), l);
  }
  assert.match(lines[0], /cli-linux-x64 /);
  assert.match(lines[1], /\/cli /);
  assert.match(lines[2], /\/api /);
  assert.equal(lines[3], 'npm dist-tag add @nearhost/cli@0.13.0 next');
  assert.equal(lines[4], 'npm dist-tag add @nearhost/api@0.13.0 next');
});

// A prerelease is published under a holding tag, and `next` moves to it only
// if it is newer, so builds that finish out of order never move it back.
test('a prerelease publishes with --tag prerelease, then may move next to it', () => {
  const lines = dryRun('0.13.0-rc.1');
  assert.equal(lines.length, 5);
  for (const l of lines.slice(0, 3)) assert.match(l, / --tag prerelease$/);
  assert.match(lines[2], /\/api /);
  assert.equal(lines[3], 'npm dist-tag add @nearhost/cli@0.13.0-rc.1 next');
  assert.equal(lines[4], 'npm dist-tag add @nearhost/api@0.13.0-rc.1 next');
});

test('a beta of 0.17.0 publishes api after cli under prerelease and moves its next', () => {
  const lines = dryRun('0.17.0-beta.1');
  assert.match(lines[2], /^npm publish .*\/api --access public --tag prerelease$/);
  assert.equal(lines[4], 'npm dist-tag add @nearhost/api@0.17.0-beta.1 next');
});

test('extra flags after -- are passed on', () => {
  const lines = dryRun('0.13.0', ['--', '--provenance']);
  for (const l of lines.filter((l) => l.startsWith('npm publish'))) assert.match(l, / --provenance$/);
});

// Runs publish.mjs for real against a fake npm, in which `npm view` finds
// nothing (or, with `published`, every version) and `npm publish` of the
// package named by `failing` prints `stderr` and exits 1. Every other publish
// prints "+ <dir name>". `npm dist-tag` prints its arguments, or fails with
// `stderr` when `failing` is "dist-tag". `npm view <pkg> dist-tags --json`
// prints `distTags`, for any package.
function run(failing, stderr, { version = '0.13.0-rc.1', published = false, distTags = { latest: '0.12.0' }, apiDistTags } = {}) {
  const root = mkdtempSync(join(tmpdir(), 'nearhost-publish-test-'));
  try {
    const out = join(root, 'out');
    for (const name of ['cli-linux-x64', 'cli', 'api']) {
      mkdirSync(join(out, name), { recursive: true });
      writeFileSync(join(out, name, 'package.json'), JSON.stringify({ name: `@nearhost/${name}`, version }));
    }
    const bin = join(root, 'bin');
    mkdirSync(bin);
    writeFileSync(
      join(bin, 'npm'),
      `#!/bin/sh
if [ "$1" = view ] && [ "$3" = dist-tags ]; then
  if [ "$2" = @nearhost/api ] && [ -n "$FAKE_NPM_API_DIST_TAGS" ]; then printf '%s\\n' "$FAKE_NPM_API_DIST_TAGS"; exit 0; fi
  printf '%s\\n' "$FAKE_NPM_DIST_TAGS"; exit 0
fi
if [ "$1" = view ]; then [ -n "$FAKE_NPM_PUBLISHED" ] && exit 0; exit 1; fi
if [ "$1" = dist-tag ]; then
  if [ "$FAKE_NPM_FAILING" = dist-tag ]; then printf '%s\\n' "$FAKE_NPM_STDERR" >&2; exit 1; fi
  echo "dist-tag $2 $3 $4"; exit 0
fi
name=$(basename "$2")
if [ "$name" = "$FAKE_NPM_FAILING" ]; then printf '%s\\n' "$FAKE_NPM_STDERR" >&2; exit 1; fi
echo "+ $name"
`,
    );
    chmodSync(join(bin, 'npm'), 0o755);
    return spawnSync(process.execPath, [publish, out], {
      encoding: 'utf8',
      env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, FAKE_NPM_FAILING: failing, FAKE_NPM_STDERR: stderr, FAKE_NPM_PUBLISHED: published ? '1' : '', FAKE_NPM_DIST_TAGS: JSON.stringify(distTags), FAKE_NPM_API_DIST_TAGS: apiDistTags ? JSON.stringify(apiDistTags) : '' },
    });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

// The fake npm is a shell script; the release workflow publishes from Linux.
const unix = { skip: process.platform === 'win32' };

// npm takes a few minutes to show a version it has accepted. A run that is
// started again inside that time must not fail on the versions it already sent.
test('a version npm is still processing is skipped, and the rest are published', unix, () => {
  const r = run(
    'cli-linux-x64',
    'npm error code E409\nnpm error 409 Conflict - PUT https://registry.npmjs.org/@nearhost%2fcli-linux-x64 - Cannot publish over previously staged version: 0.13.0-rc.1',
  );
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /@nearhost\/cli-linux-x64@0\.13\.0-rc\.1 is already with npm, which is still processing it; skipping/);
  assert.match(r.stdout, /^\+ cli$/m);
  assert.match(r.stderr, /E409/);
});

test('any other publish failure stops the run and shows npm\'s error', unix, () => {
  const r = run('cli-linux-x64', 'npm error code E403\nnpm error 403 Forbidden - PUT https://registry.npmjs.org/@nearhost%2fcli-linux-x64');
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /E403/);
  assert.doesNotMatch(r.stdout, /^\+ cli$/m);
});

// A rerun after the publishes went through, but moving `next` did not, must
// still move it.
test('a normal version already published still moves next to it', unix, () => {
  const r = run('', '', { version: '0.13.0', published: true, distTags: { latest: '0.13.0', next: '0.13.0-rc.2' } });
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /@nearhost\/cli@0\.13\.0 is already published; skipping/);
  assert.match(r.stdout, /^dist-tag add @nearhost\/cli@0\.13\.0 next$/m);
});

// Which version `next` ends on, for this version and the tags npm has.
function nextAfter(version, distTags) {
  const r = run('', '', { version, distTags });
  assert.equal(r.status, 0, r.stderr);
  const moved = r.stdout.match(/^dist-tag add @nearhost\/cli@(\S+) next$/m);
  return moved ? moved[1] : distTags.next;
}

test('next moves only to a newer version than next and latest', unix, () => {
  assert.equal(nextAfter('0.13.0-rc.1', { latest: '0.12.0' }), '0.13.0-rc.1');
  assert.equal(nextAfter('0.13.0-rc.2', { latest: '0.12.0', next: '0.13.0-rc.1' }), '0.13.0-rc.2');
  assert.equal(nextAfter('0.13.0-rc.10', { latest: '0.12.0', next: '0.13.0-rc.9' }), '0.13.0-rc.10');
  assert.equal(nextAfter('0.14.0-beta.1', { latest: '0.13.0', next: '0.13.0' }), '0.14.0-beta.1');
  // A slower build finishing after a newer one.
  assert.equal(nextAfter('0.13.0-rc.1', { latest: '0.12.0', next: '0.13.0-rc.2' }), '0.13.0-rc.2');
  // A beta finishing after its release.
  assert.equal(nextAfter('0.13.0-beta.3', { latest: '0.13.0', next: '0.13.0' }), '0.13.0');
  assert.equal(nextAfter('0.13.0-beta.3', { latest: '0.13.0', next: '0.13.0-beta.2' }), '0.13.0');
  // A rerun of an older release.
  assert.equal(nextAfter('0.12.0', { latest: '0.13.0', next: '0.14.0-beta.1' }), '0.14.0-beta.1');
  // Already there: nothing to move.
  assert.equal(nextAfter('0.13.0', { latest: '0.13.0', next: '0.13.0' }), '0.13.0');
});

// @nearhost/api has its own tags, and the same forward-only rule applies to it.
test('next of @nearhost/api moves forward only, on its own tags', unix, () => {
  const move = (version, apiDistTags) => {
    const r = run('', '', { version, distTags: { latest: '0.12.0' }, apiDistTags });
    assert.equal(r.status, 0, r.stderr);
    return r.stdout.match(/^dist-tag add @nearhost\/api@(\S+) next$/m)?.[1] ?? apiDistTags.next;
  };
  assert.equal(move('0.17.0-beta.2', { latest: '0.16.0', next: '0.17.0-beta.1' }), '0.17.0-beta.2');
  assert.equal(move('0.17.0-beta.1', { latest: '0.16.0', next: '0.17.0-beta.2' }), '0.17.0-beta.2');
  assert.equal(move('0.17.0-beta.3', { latest: '0.17.0', next: '0.17.0' }), '0.17.0');
});

test('a failed dist-tag fails the run and shows npm\'s error', unix, () => {
  const r = run('dist-tag', 'npm error code E403', { version: '0.13.0' });
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /E403/);
});
