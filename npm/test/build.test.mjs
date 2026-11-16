import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const build = join(dirname(fileURLToPath(import.meta.url)), '..', 'build.mjs');
const VERSION = '0.13.0';

const have = (cmd) => spawnSync(cmd, ['-v'], { stdio: 'ignore' }).error === undefined;
// The release job runs on ubuntu-latest, where both exist.
const zipSkip = have('zip') && have('unzip') ? false : 'zip and unzip are not installed';

// Makes the archive for one target, holding a stand-in binary.
function archive(dir, goos, goarch) {
  const stage = mkdtempSync(join(dir, 'stage-'));
  const exe = goos === 'windows' ? 'nearhost.exe' : 'nearhost';
  writeFileSync(join(stage, exe), `binary ${goos} ${goarch}\n`);
  writeFileSync(join(stage, 'LICENSE'), 'MIT\n');
  if (goos === 'windows') {
    const name = `nearhost_${VERSION}_${goos}_${goarch}.zip`;
    execFileSync('zip', ['-q', join(dir, name), exe, 'LICENSE'], { cwd: stage });
    return name;
  }
  const name = `nearhost_${VERSION}_${goos}_${goarch}.tar.gz`;
  execFileSync('tar', ['-czf', join(dir, name), '-C', stage, exe, 'LICENSE']);
  return name;
}

// Serves a fake release with the given assets and runs build.mjs against it.
// The child is async so this process can keep answering its requests.
async function run(assets, { corrupt = [] } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'nearhost-build-test-'));
  const files = new Map();
  const lines = [];
  for (const [goos, goarch] of assets) {
    const name = archive(dir, goos, goarch);
    const data = readFileSync(join(dir, name));
    files.set(name, data);
    const sum = createHash('sha256').update(data).digest('hex');
    lines.push(`${corrupt.includes(name) ? '0'.repeat(64) : sum}  ${name}`);
  }
  files.set('checksums.txt', Buffer.from(lines.join('\n') + '\n'));
  const server = createServer((req, res) => {
    const f = files.get(req.url.slice(1));
    res.statusCode = f ? 200 : 404;
    res.end(f);
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  const out = join(dir, 'out');
  const env = { ...process.env, NEARHOST_BASE_URL: `http://127.0.0.1:${server.address().port}` };
  const result = await new Promise((resolve) => {
    const p = spawn(process.execPath, [build, VERSION, out], { env, stdio: ['ignore', 'pipe', 'pipe'] });
    let stderr = '';
    p.stderr.on('data', (d) => (stderr += d));
    p.stdout.resume();
    p.on('close', (status) => resolve({ status, stderr }));
  });
  server.close();
  return { ...result, out, cleanup: () => rmSync(dir, { recursive: true, force: true }) };
}

const unix = [['darwin', 'amd64'], ['darwin', 'arm64'], ['linux', 'amd64'], ['linux', 'arm64']];
const windows = [['windows', 'amd64'], ['windows', 'arm64']];
const json = (p) => JSON.parse(readFileSync(p, 'utf8'));

test('four Unix assets give four platform packages and a launcher that lists them', async () => {
  const r = await run(unix);
  try {
    assert.equal(r.status, 0, r.stderr);
    assert.deepEqual(readdirSync(r.out).sort(), [
      'cli', 'cli-darwin-arm64', 'cli-darwin-x64', 'cli-linux-arm64', 'cli-linux-x64',
    ]);
    const deps = json(join(r.out, 'cli', 'package.json')).optionalDependencies;
    assert.deepEqual(Object.keys(deps).sort(), [
      '@nearhost/cli-darwin-arm64', '@nearhost/cli-darwin-x64', '@nearhost/cli-linux-arm64', '@nearhost/cli-linux-x64',
    ]);
    assert.ok(existsSync(join(r.out, 'cli-linux-x64', 'bin', 'nearhost')));
  } finally {
    r.cleanup();
  }
});

test('six assets give six packages, with nearhost.exe for Windows', { skip: zipSkip }, async () => {
  const r = await run([...unix, ...windows]);
  try {
    assert.equal(r.status, 0, r.stderr);
    const deps = json(join(r.out, 'cli', 'package.json')).optionalDependencies;
    assert.equal(Object.keys(deps).length, 6);
    for (const cpu of ['x64', 'arm64']) {
      const dir = join(r.out, `cli-win32-${cpu}`);
      const pkg = json(join(dir, 'package.json'));
      assert.equal(pkg.name, `@nearhost/cli-win32-${cpu}`);
      assert.deepEqual(pkg.os, ['win32']);
      assert.deepEqual(pkg.cpu, [cpu]);
      assert.equal(deps[pkg.name], VERSION);
      assert.equal(readFileSync(join(dir, 'bin', 'nearhost.exe'), 'utf8').startsWith('binary windows'), true);
      assert.deepEqual(readdirSync(join(dir, 'bin')), ['nearhost.exe']);
      const readme = readFileSync(join(dir, 'README.md'), 'utf8');
      assert.match(readme, new RegExp(`nearhost_${VERSION}_windows_${cpu === 'x64' ? 'amd64' : 'arm64'}\\.zip`));
    }
  } finally {
    r.cleanup();
  }
});

test('a missing Linux or macOS asset still fails', async () => {
  for (const missing of ['linux', 'darwin']) {
    const r = await run(unix.filter(([os, arch]) => !(os === missing && arch === 'arm64')));
    try {
      assert.notEqual(r.status, 0);
      assert.match(r.stderr, new RegExp(`${missing}_arm64`));
    } finally {
      r.cleanup();
    }
  }
});

test('a checksum mismatch on a zip fails', { skip: zipSkip }, async () => {
  const r = await run([...unix, ...windows], { corrupt: [`nearhost_${VERSION}_windows_arm64.zip`] });
  try {
    assert.notEqual(r.status, 0);
    assert.match(r.stderr, /windows_arm64\.zip: checksum mismatch/);
  } finally {
    r.cleanup();
  }
});

test('only one Windows asset is built when the other is not listed', { skip: zipSkip }, async () => {
  const r = await run([...unix, windows[0]]);
  try {
    assert.equal(r.status, 0, r.stderr);
    assert.ok(existsSync(join(r.out, 'cli-win32-x64')));
    assert.ok(!existsSync(join(r.out, 'cli-win32-arm64')));
  } finally {
    r.cleanup();
  }
});
