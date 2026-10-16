#!/usr/bin/env node
// Runs the nearhost binary from the platform package npm installed next to
// this one. Each @nearhost/cli-<os>-<arch> package declares os and cpu, so
// npm installs only the one that matches this machine.
'use strict';

const { spawn } = require('node:child_process');

const pkg = `@nearhost/cli-${process.platform}-${process.arch}`;

let bin;
try {
  bin = require.resolve(`${pkg}/bin/nearhost`);
} catch {
  const supported = ['darwin', 'linux'].includes(process.platform) && ['x64', 'arm64'].includes(process.arch);
  console.error(
    supported
      ? `nearhost: ${pkg} is not installed. It is an optional dependency of @nearhost/cli; reinstall without --no-optional or --omit=optional.`
      : `nearhost: there is no build for ${process.platform} on ${process.arch}. Nearhost runs on macOS and Linux, on x64 and arm64.`,
  );
  process.exit(1);
}

const child = spawn(bin, process.argv.slice(2), { stdio: 'inherit' });

// Ctrl-C and a closing terminal reach nearhost directly, as they reach every
// process in the foreground group; passing them on as well would count as a
// second signal, which makes `nearhost up` cut its shutdown short. A SIGTERM
// is sent to this process alone, so it is passed on.
process.on('SIGINT', () => {});
process.on('SIGHUP', () => {});
process.on('SIGTERM', () => child.kill('SIGTERM'));

child.on('error', (err) => {
  console.error(`nearhost: could not run ${bin}: ${err.message}`);
  process.exit(1);
});

child.on('exit', (code, signal) => {
  if (signal) {
    process.removeAllListeners(signal);
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code);
});
