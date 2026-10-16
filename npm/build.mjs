#!/usr/bin/env node
// Assembles the npm packages for a release that is already on GitHub:
// @nearhost/cli and one @nearhost/cli-<os>-<arch> package per binary.
//
//   node npm/build.mjs <version> [out dir]
//
// Each binary comes from the release's tarball, checked against its
// checksums.txt. The packages land in the out dir, npm/dist by default,
// ready for npm/publish.mjs.
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { chmodSync, cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));

// GOOS/GOARCH as goreleaser names the tarballs, and the platform and arch
// Node reports for them.
const targets = [
  { goos: 'darwin', goarch: 'amd64', os: 'darwin', cpu: 'x64' },
  { goos: 'darwin', goarch: 'arm64', os: 'darwin', cpu: 'arm64' },
  { goos: 'linux', goarch: 'amd64', os: 'linux', cpu: 'x64' },
  { goos: 'linux', goarch: 'arm64', os: 'linux', cpu: 'arm64' },
];

const version = (process.argv[2] ?? '').replace(/^v/, '');
if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version)) {
  console.error('usage: node npm/build.mjs <version> [out dir]');
  process.exit(2);
}
const out = process.argv[3] ?? join(here, 'dist');
const base = process.env.NEARHOST_BASE_URL ?? `https://github.com/nearhost/nearhost/releases/download/v${version}`;

async function download(name) {
  const res = await fetch(`${base}/${name}`);
  if (!res.ok) throw new Error(`download failed: ${base}/${name}: ${res.status} ${res.statusText}`);
  return Buffer.from(await res.arrayBuffer());
}

const sums = new Map(
  (await download('checksums.txt'))
    .toString()
    .trim()
    .split('\n')
    .map((line) => line.split(/\s+/).reverse()),
);

const launcher = JSON.parse(readFileSync(join(here, 'cli', 'package.json'), 'utf8'));
const shared = {
  version,
  homepage: launcher.homepage,
  bugs: launcher.bugs,
  repository: launcher.repository,
  license: launcher.license,
};

rmSync(out, { recursive: true, force: true });
const work = mkdtempSync(join(tmpdir(), 'nearhost-npm-'));

try {
  for (const t of targets) {
    const asset = `nearhost_${version}_${t.goos}_${t.goarch}.tar.gz`;
    const tarball = await download(asset);
    const sum = createHash('sha256').update(tarball).digest('hex');
    if (!sums.has(asset)) throw new Error(`${asset} is not in checksums.txt`);
    if (sums.get(asset) !== sum) throw new Error(`${asset}: checksum mismatch`);

    const name = `cli-${t.os}-${t.cpu}`;
    const dir = join(out, name);
    mkdirSync(join(dir, 'bin'), { recursive: true });
    writeFileSync(join(work, asset), tarball);
    execFileSync('tar', ['-xzf', join(work, asset), '-C', join(dir, 'bin'), 'nearhost']);
    chmodSync(join(dir, 'bin', 'nearhost'), 0o755);

    const pkg = {
      name: `@nearhost/${name}`,
      description: `The nearhost binary for ${t.os} on ${t.cpu}, installed by @nearhost/cli`,
      ...shared,
      repository: { ...shared.repository, directory: 'npm' },
      os: [t.os],
      cpu: [t.cpu],
      files: ['bin'],
      preferUnplugged: true,
    };
    writeFileSync(join(dir, 'package.json'), JSON.stringify(pkg, null, 2) + '\n');
    writeFileSync(
      join(dir, 'README.md'),
      `# @nearhost/${name}\n\nThe [Nearhost](https://nearhost.dev) binary for ${t.os} on ${t.cpu}. Install [\`@nearhost/cli\`](https://www.npmjs.com/package/@nearhost/cli) rather than this package; it picks the right one for your machine.\n`,
    );
    launcher.optionalDependencies[pkg.name] = version;
    console.log(`${pkg.name}@${version}`);
  }

  const dir = join(out, 'cli');
  cpSync(join(here, 'cli'), dir, { recursive: true });
  cpSync(join(here, '..', 'LICENSE'), join(dir, 'LICENSE'));
  writeFileSync(join(dir, 'package.json'), JSON.stringify({ ...launcher, version }, null, 2) + '\n');
  for (const t of targets) cpSync(join(here, '..', 'LICENSE'), join(out, `cli-${t.os}-${t.cpu}`, 'LICENSE'));
  console.log(`${launcher.name}@${version}`);
} finally {
  rmSync(work, { recursive: true, force: true });
}
