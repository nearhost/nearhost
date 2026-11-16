import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
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
