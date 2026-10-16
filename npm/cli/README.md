# @nearhost/cli

[Nearhost](https://nearhost.dev) gives every local and remote development server an address at `https://<name>.test`, with a certificate your browser trusts and SSH forwards that look after themselves.

```sh
npx @nearhost/cli
```

or install it for good:

```sh
npm install -g @nearhost/cli
nearhost
```

This package holds a small launcher. The binary comes in a platform package, `@nearhost/cli-<os>-<arch>`, which npm picks for your machine: macOS or Linux, on x64 or arm64. Nothing is downloaded by an install script, so it works with `--ignore-scripts`, pnpm and bun.

On Linux, Nearhost needs permission to bind ports 80 and 443. `nearhost doctor` tells you how to grant it.

Read the [docs](https://nearhost.dev/docs/) for the rest. The source is at [github.com/nearhost/nearhost](https://github.com/nearhost/nearhost).
