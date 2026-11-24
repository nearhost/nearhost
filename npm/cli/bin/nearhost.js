#!/usr/bin/env node
// Runs the nearhost binary from the platform package npm installed next to
// this one. Each @nearhost/cli-<os>-<arch> package declares os and cpu, so
// npm installs only the one that matches this machine.
'use strict';

const { spawn } = require('node:child_process');

const pkg = `@nearhost/cli-${process.platform}-${process.arch}`;
const exe = process.platform === 'win32' ? 'nearhost.exe' : 'nearhost';

// The builds this release has are the platform packages npm/build.mjs put in
// our own optionalDependencies, so a release without a Windows build says so.
const builds = Object.keys(require('../package.json').optionalDependencies ?? {}).map((n) =>
  n.replace('@nearhost/cli-', '').replace('-', ' on '),
);

const supported = builds.includes(`${process.platform} on ${process.arch}`);

let bin;
try {
  if (supported) bin = require.resolve(`${pkg}/bin/${exe}`);
} catch {
  // Reported below.
}
if (!bin) {
  console.error(
    supported
      ? `nearhost: ${pkg} is not installed. It is an optional dependency of @nearhost/cli; reinstall without --no-optional or --omit=optional.`
      : `nearhost: this release has no build for ${process.platform} on ${process.arch}. It has builds for ${builds.join(', ')}.`,
  );
  process.exit(1);
}

// Smart App Control and some antivirus programs refuse to run a program that
// is not signed. Node reports that as UNKNOWN, EPERM or EACCES, from spawn
// itself or in the error event.
function fail(err) {
  console.error(`nearhost: could not run ${bin}: ${err.message}`);
  if (process.platform === 'win32' && ['UNKNOWN', 'EPERM', 'EACCES'].includes(err.code)) {
    console.error('Windows may have blocked it: Smart App Control and some antivirus block programs that are not signed.');
  }
  process.exit(1);
}

let child;
try {
  child = spawn(bin, process.argv.slice(2), { stdio: 'inherit' });
} catch (err) {
  fail(err);
}

// Ctrl-C and a closing terminal reach nearhost directly, as they reach every
// process in the foreground group; passing them on as well would count as a
// second signal, which makes `nearhost up` cut its shutdown short. A SIGTERM
// is sent to this process alone, so it is passed on.
process.on('SIGINT', () => {});
process.on('SIGHUP', () => {});
process.on('SIGTERM', () => child.kill('SIGTERM'));
// On Windows, Ctrl-Break reaches nearhost.exe directly, as Ctrl-C does.
if (process.platform === 'win32') process.on('SIGBREAK', () => {});

child.on('error', fail);

child.on('exit', (code, signal) => {
  if (signal) {
    process.removeAllListeners(signal);
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code);
});
