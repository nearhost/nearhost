# @nearhost/{{name}}

The [Nearhost](https://nearhost.dev) binary for {{label}}. You don't install this package yourself: install [`@nearhost/cli`](https://www.npmjs.com/package/@nearhost/cli), and npm picks this package when it matches your machine.

```sh
npx @nearhost/cli
```

Nearhost is a terminal app that opens a dev server running on one of your other machines at a local `https://myapp.test`, over the SSH config you already have. You install nothing on the server, and cookies, OAuth redirects, WebSockets and hot reload behave as they do on `localhost`.

- **One address per project**, with a certificate from a local certificate authority your browser trusts.
- **Your own SSH.** Nearhost runs OpenSSH, so your `~/.ssh/config`, agent and jump hosts apply.
- **Any stack.** If it listens on a port, Nearhost can serve it, on a server or on this machine.

[Website](https://nearhost.dev) · [Docs](https://nearhost.dev/docs/getting-started/introduction/) · [GitHub](https://github.com/nearhost/nearhost)

This binary matches the `nearhost_{{version}}_{{goos}}_{{goarch}}.tar.gz` asset on the [v{{version}} release](https://github.com/nearhost/nearhost/releases/tag/v{{version}}) byte for byte. MIT licence.
