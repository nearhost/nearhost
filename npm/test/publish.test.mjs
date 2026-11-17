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
    for (const name of ['cli-linux-x64', 'cli']) {
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

test('a normal version publishes with no --tag', () => {
  const lines = dryRun('0.13.0');
  assert.equal(lines.length, 2);
  for (const l of lines) {
    assert.match(l, /^npm publish .* --access public/);
    assert.ok(!l.includes('--tag'), l);
  }
});

test('a prerelease publishes with --tag next', () => {
  const lines = dryRun('0.13.0-rc.1');
  assert.equal(lines.length, 2);
  for (const l of lines) assert.match(l, / --tag next$/);
});

test('extra flags after -- are passed on', () => {
  const lines = dryRun('0.13.0', ['--', '--provenance']);
  for (const l of lines) assert.match(l, / --provenance$/);
});

// Runs publish.mjs for real against a fake npm, in which `npm view` finds
// nothing and `npm publish` of the package named by `failing` prints `stderr`
// and exits 1. Every other publish prints "+ <dir name>".
function run(failing, stderr) {
  const root = mkdtempSync(join(tmpdir(), 'nearhost-publish-test-'));
  try {
    const out = join(root, 'out');
    for (const name of ['cli-linux-x64', 'cli']) {
      mkdirSync(join(out, name), { recursive: true });
      writeFileSync(join(out, name, 'package.json'), JSON.stringify({ name: `@nearhost/${name}`, version: '0.13.0-rc.1' }));
    }
    const bin = join(root, 'bin');
    mkdirSync(bin);
    writeFileSync(
      join(bin, 'npm'),
      `#!/bin/sh
[ "$1" = view ] && exit 1
name=$(basename "$2")
if [ "$name" = "$FAKE_NPM_FAILING" ]; then printf '%s\\n' "$FAKE_NPM_STDERR" >&2; exit 1; fi
echo "+ $name"
`,
    );
    chmodSync(join(bin, 'npm'), 0o755);
    return spawnSync(process.execPath, [publish, out], {
      encoding: 'utf8',
      env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, FAKE_NPM_FAILING: failing, FAKE_NPM_STDERR: stderr },
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
