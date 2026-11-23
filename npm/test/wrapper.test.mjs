import { after, test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, mkdirSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
// Under the repo, not os.tmpdir(): on Windows runners that is an 8.3 short path.
const work = realpathSync.native(mkdtempSync(join(root, 'npm', 'test', '.tmp-wrapper-')));
after(() => rmSync(work, { recursive: true, force: true }));

// Runs the wrapper under a faked process.platform with child_process.spawn
// replaced, so the test needs no real binary. The preload runs before the
// wrapper requires child_process.
function run(platform, spawnBody) {
  const dir = join(work, platform + Math.random().toString(36).slice(2));
  const modules = join(dir, 'node_modules', '@nearhost');
  const exe = platform === 'win32' ? 'nearhost.exe' : 'nearhost';
  const pkg = `cli-${platform}-${process.arch}`;
  mkdirSync(join(modules, 'cli', 'bin'), { recursive: true });
  mkdirSync(join(modules, pkg, 'bin'), { recursive: true });
  cpSync(join(root, 'npm', 'cli', 'bin', 'nearhost.js'), join(modules, 'cli', 'bin', 'nearhost.js'));
  writeFileSync(
    join(modules, 'cli', 'package.json'),
    JSON.stringify({ name: '@nearhost/cli', version: '1.2.3', optionalDependencies: { [`@nearhost/${pkg}`]: '1.2.3' } }),
  );
  writeFileSync(join(modules, pkg, 'package.json'), JSON.stringify({ name: `@nearhost/${pkg}`, version: '1.2.3' }));
  writeFileSync(join(modules, pkg, 'bin', exe), '');
  const preload = join(dir, 'preload.cjs');
  writeFileSync(
    preload,
    `Object.defineProperty(process, 'platform', { value: ${JSON.stringify(platform)} });
require('node:child_process').spawn = () => { ${spawnBody} };`,
  );
  const r = spawnSync(process.execPath, ['--require', preload, join(modules, 'cli', 'bin', 'nearhost.js')], {
    encoding: 'utf8',
  });
  return { ...r, bin: join(modules, pkg, 'bin', exe) };
}

const blocked = 'Windows may have blocked it: Smart App Control and some antivirus block programs that are not signed.';
const throwing = "const e = new Error('spawn UNKNOWN'); e.code = 'UNKNOWN'; throw e;";

test('a spawn that throws on Windows says it may have been blocked', () => {
  const r = run('win32', throwing);
  assert.equal(r.status, 1);
  assert.equal(r.stderr, `nearhost: could not run ${r.bin}: spawn UNKNOWN\n${blocked}\n`);
});

test('a spawn that throws elsewhere has only the first line', () => {
  const r = run('linux', throwing);
  assert.equal(r.status, 1);
  assert.equal(r.stderr, `nearhost: could not run ${r.bin}: spawn UNKNOWN\n`);
});

test('a Windows error that is not a block has only the first line', () => {
  const r = run('win32', "const e = new Error('spawn ENOENT'); e.code = 'ENOENT'; throw e;");
  assert.equal(r.status, 1);
  assert.equal(r.stderr, `nearhost: could not run ${r.bin}: spawn ENOENT\n`);
});

test('the asynchronous error event gets the same second line', () => {
  const r = run(
    'win32',
    `const { EventEmitter } = require('node:events');
const c = new EventEmitter();
setImmediate(() => { const e = new Error('spawn EPERM'); e.code = 'EPERM'; c.emit('error', e); });
return c;`,
  );
  assert.equal(r.status, 1);
  assert.equal(r.stderr, `nearhost: could not run ${r.bin}: spawn EPERM\n${blocked}\n`);
});
