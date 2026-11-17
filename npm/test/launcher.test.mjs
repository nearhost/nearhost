import { after, before, test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { cpSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { delimiter, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const platform = `${process.platform}-${process.arch}`;
const exe = process.platform === 'win32' ? 'nearhost.exe' : 'nearhost';
const goBin = join(homedir(), '.local', 'go', 'bin');

let work;
let binary;

before(() => {
  work = mkdtempSync(join(tmpdir(), 'nearhost-launcher-test-'));
  binary = join(work, exe);
  execFileSync('go', ['build', '-o', binary, './cmd/nearhost'], {
    cwd: root,
    env: { ...process.env, PATH: `${process.env.PATH}${delimiter}${goBin}` },
  });
});

after(() => rmSync(work, { recursive: true, force: true }));

// A node_modules tree as npm lays it out: the launcher, and optionally the
// platform package for this machine. `deps` is the launcher's
// optionalDependencies.
function install(name, { deps, withPlatform = true }) {
  const modules = join(work, name, 'node_modules', '@nearhost');
  mkdirSync(join(modules, 'cli', 'bin'), { recursive: true });
  cpSync(join(root, 'npm', 'cli', 'bin', 'nearhost.js'), join(modules, 'cli', 'bin', 'nearhost.js'));
  writeFileSync(
    join(modules, 'cli', 'package.json'),
    JSON.stringify({ name: '@nearhost/cli', version: '1.2.3', optionalDependencies: deps }),
  );
  if (withPlatform) {
    const dir = join(modules, `cli-${platform}`);
    mkdirSync(join(dir, 'bin'), { recursive: true });
    cpSync(binary, join(dir, 'bin', exe));
    writeFileSync(join(dir, 'package.json'), JSON.stringify({ name: `@nearhost/cli-${platform}`, version: '1.2.3' }));
  }
  return join(modules, 'cli', 'bin', 'nearhost.js');
}

const launch = (script, ...args) => spawnSync(process.execPath, [script, ...args], { encoding: 'utf8' });
const mine = { [`@nearhost/cli-${platform}`]: '1.2.3' };

test('version prints the binary version and exits 0', () => {
  const r = launch(install('ok', { deps: mine }), 'version');
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /^nearhost dev/);
});

test('the exit code of a failing command comes through', () => {
  const r = launch(install('fail', { deps: mine }), 'no-such-command');
  assert.equal(r.status, 1);
  assert.match(r.stderr, /unknown command/);
});

test('a missing platform package is named, with the flag to avoid', () => {
  const r = launch(install('missing', { deps: mine, withPlatform: false }), 'version');
  assert.equal(r.status, 1);
  assert.ok(r.stderr.includes(`@nearhost/cli-${platform}`), r.stderr);
  assert.ok(r.stderr.includes('--omit=optional'), r.stderr);
});

test('a platform this release has no build for says so and lists the builds it has', () => {
  const deps = { '@nearhost/cli-linux-x64': '1.2.3', '@nearhost/cli-darwin-arm64': '1.2.3' };
  delete deps[`@nearhost/cli-${platform}`];
  const r = launch(install('absent', { deps, withPlatform: false }), 'version');
  assert.equal(r.status, 1);
  assert.ok(r.stderr.includes('this release has no build for'), r.stderr);
  assert.ok(r.stderr.includes(`${process.platform} on ${process.arch}`), r.stderr);
  assert.ok(r.stderr.includes('linux on x64') && r.stderr.includes('darwin on arm64'), r.stderr);
  assert.ok(!r.stderr.includes('macOS and Linux'), r.stderr);
  assert.ok(!r.stderr.includes('--omit=optional'), r.stderr);
});
