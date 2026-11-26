#!/usr/bin/env node
// Assembles the npm packages for a release that is already on GitHub:
// @nearhost/cli and one @nearhost/cli-<os>-<arch> package per binary, and
// @nearhost/api, the control API's OpenAPI schema and TypeScript types.
//
//   node npm/build.mjs <version> [out dir]
//
// Each binary comes from the release's tarball, checked against its
// checksums.txt. The api package's files come from this checkout (api/openapi.yaml,
// api/ts/index.d.ts, api/ts/README.md), so run it on the release's tag. The packages land in the out dir, npm/dist by default,
// ready for npm/publish.mjs.
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { chmodSync, cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));

// GOOS/GOARCH as goreleaser names the archives, and the platform and arch
// Node reports for them. Windows is built only when the release has its
// archives (a release made with the Windows switch off does not).
const targets = [
  { goos: 'darwin', goarch: 'amd64', os: 'darwin', cpu: 'x64', label: 'macOS on Intel' },
  { goos: 'darwin', goarch: 'arm64', os: 'darwin', cpu: 'arm64', label: 'macOS on Apple silicon' },
  { goos: 'linux', goarch: 'amd64', os: 'linux', cpu: 'x64', label: 'Linux on x64' },
  { goos: 'linux', goarch: 'arm64', os: 'linux', cpu: 'arm64', label: 'Linux on ARM64' },
  { goos: 'windows', goarch: 'amd64', os: 'win32', cpu: 'x64', label: 'Windows on x64', optional: true },
  { goos: 'windows', goarch: 'arm64', os: 'win32', cpu: 'arm64', label: 'Windows on ARM64', optional: true },
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

const readme = readFileSync(join(here, 'platform', 'README.md'), 'utf8');
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
  const built = [];
  for (const t of targets) {
    const zip = t.goos === 'windows';
    const ext = zip ? 'zip' : 'tar.gz';
    const exe = zip ? 'nearhost.exe' : 'nearhost';
    const asset = `nearhost_${version}_${t.goos}_${t.goarch}.${ext}`;
    if (t.optional && !sums.has(asset)) continue;
    built.push(t);
    const tarball = await download(asset);
    const sum = createHash('sha256').update(tarball).digest('hex');
    if (!sums.has(asset)) throw new Error(`${asset} is not in checksums.txt`);
    if (sums.get(asset) !== sum) throw new Error(`${asset}: checksum mismatch`);

    const name = `cli-${t.os}-${t.cpu}`;
    const dir = join(out, name);
    mkdirSync(join(dir, 'bin'), { recursive: true });
    writeFileSync(join(work, asset), tarball);
    if (zip) execFileSync('unzip', ['-q', '-o', join(work, asset), exe, '-d', join(dir, 'bin')]);
    else execFileSync('tar', ['-xzf', join(work, asset), '-C', join(dir, 'bin'), exe]);
    if (!zip) chmodSync(join(dir, 'bin', exe), 0o755);

    const pkg = {
      name: `@nearhost/${name}`,
      description: `The Nearhost binary for ${t.label}, installed by @nearhost/cli`,
      ...shared,
      repository: { ...shared.repository, directory: 'npm' },
      os: [t.os],
      cpu: [t.cpu],
      files: ['bin'],
      preferUnplugged: true,
    };
    writeFileSync(join(dir, 'package.json'), JSON.stringify(pkg, null, 2) + '\n');
    const fields = { name, label: t.label, version, goos: t.goos, goarch: t.goarch, ext };
    writeFileSync(join(dir, 'README.md'), readme.replace(/{{(\w+)}}/g, (_, k) => fields[k]));
    launcher.optionalDependencies[pkg.name] = version;
    console.log(`${pkg.name}@${version}`);
  }

  const dir = join(out, 'cli');
  cpSync(join(here, 'cli'), dir, { recursive: true });
  cpSync(join(here, '..', 'LICENSE'), join(dir, 'LICENSE'));
  writeFileSync(join(dir, 'package.json'), JSON.stringify({ ...launcher, version }, null, 2) + '\n');
  for (const t of built) cpSync(join(here, '..', 'LICENSE'), join(out, `cli-${t.os}-${t.cpu}`, 'LICENSE'));
  console.log(`${launcher.name}@${version}`);

  const api = join(out, 'api');
  const apiRoot = join(here, '..', 'api');
  mkdirSync(api, { recursive: true });
  cpSync(join(apiRoot, 'openapi.yaml'), join(api, 'openapi.yaml'));
  for (const f of ['index.d.ts', 'README.md']) cpSync(join(apiRoot, 'ts', f), join(api, f));
  cpSync(join(here, '..', 'LICENSE'), join(api, 'LICENSE'));
  const apiPkg = {
    name: '@nearhost/api',
    description: "The Nearhost control API: its OpenAPI 3.1 schema and TypeScript types",
    keywords: ['nearhost', 'openapi', 'typescript', 'types'],
    ...shared,
    repository: { ...shared.repository, directory: 'api/ts' },
    author: launcher.author,
    types: 'index.d.ts',
    exports: {
      '.': { types: './index.d.ts' },
      './openapi.yaml': './openapi.yaml',
      './package.json': './package.json',
    },
    files: ['openapi.yaml', 'index.d.ts', 'README.md'],
  };
  writeFileSync(join(api, 'package.json'), JSON.stringify(apiPkg, null, 2) + '\n');
  console.log(`${apiPkg.name}@${version}`);
} finally {
  rmSync(work, { recursive: true, force: true });
}
