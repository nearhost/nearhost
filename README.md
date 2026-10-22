<a href="https://nearhost.dev"><img src="https://raw.githubusercontent.com/nearhost/.github/main/assets/header.png" alt="Nearhost. Run it anywhere. Open it here."></a>

# Nearhost

Nearhost is a terminal app that opens a dev server running on one of your other machines at a local `https://myapp.test`, over the SSH config you already have. You install nothing on the server, and cookies, OAuth redirects, WebSockets and hot reload behave as they do on `localhost`.

```sh
curl -fsSL https://nearhost.dev/install.sh | sh
```

[Website](https://nearhost.dev) · [Docs](https://nearhost.dev/docs/getting-started/introduction/) · [Releases](https://github.com/nearhost/nearhost/releases) · [npm](https://www.npmjs.com/package/@nearhost/cli)

![The Nearhost terminal interface: servers and projects on the left, and the selected project's address, routes and request log on the right.](https://raw.githubusercontent.com/nearhost/.github/main/assets/tui.png)

## Why Nearhost

You write code on a laptop and run it somewhere else: a desktop under the desk, a home server, a VPS, or a container on this machine. The app listens on `localhost` over there, and your browser is over here. That usually means an `ssh -L` per port, a certificate warning and a hot reload socket that fails to connect.

Nearhost gives each project a name, a certificate your browser trusts and forwards that look after themselves. Add a project once and it is at `https://myapp.test` today, tomorrow and after a reboot.

- **One address per project.** Every project gets a stable `.test` alias, so bookmarks, cookies and OAuth callbacks keep working.
- **Real HTTPS, trusted once.** Nearhost creates its own local certificate authority on first run, and it signs a certificate for each alias. Secure cookies, service workers and anything else that needs a secure context behave as they do in production. No shared key ships in the binary, so nobody else can mint certificates for your names.
- **Remote servers over the SSH you already have.** Nearhost runs your own `ssh`, so your `~/.ssh/config`, agent and jump hosts all apply. Nothing is installed on the server and no ports are opened there; Nearhost only keeps a socket in `~/.nearhost`, so a coding agent on the server can reach it.
- **Hot reload and WebSockets pass through.** Upgrades travel through the same connection as the page.
- **Works with any stack.** If it listens on a port, Nearhost can serve it. It reads the port variables in your `.env` and proposes routes with no framework knowledge at all.
- **Local projects too.** The `local` server needs no setup. Projects on your laptop get the same alias and certificate, so one tool covers the laptop and the servers.
- **Presets when you want them.** Framework-specific setups, such as Docker Compose, Laravel Sail or Vite, come from preset registries. No registry is trusted until you add one, and you can publish and trust your own.
- **Your traffic stays yours.** No relay, no account and no public URL. Requests travel over your own SSH connection.
- **Tidy about your machine.** Nearhost owns one marked block in `/etc/hosts`, with each name on `127.0.0.1` and `::1`, asks for `sudo` only when that block changes, and removes it again with `nearhost hosts clean`.
- **Runs in the foreground.** `nearhost up` serves until you press Ctrl-C, and the forwards close with it. `nearhost doctor` tells you what is wrong, and how to fix it, before you start.

## Install

Nearhost is a single binary for macOS and Linux, on Intel and ARM.

**Homebrew**

```sh
brew install nearhost/tap/nearhost
```

**Shell installer**

```sh
curl -fsSL https://nearhost.dev/install.sh | sh
```

The script downloads the matching release from GitHub, checks it against the published checksums and installs to `/usr/local/bin` when that is writable, otherwise to `~/.local/bin`. On Linux it also offers to let the binary bind ports 80 and 443, and runs `sudo setcap` only if you say yes. It asks only when it can read your answer from a terminal, as when you run a downloaded copy; through `curl | sh` it prints the command instead.

**npm**

```sh
npx @nearhost/cli
```

or `npm install -g @nearhost/cli` to keep the `nearhost` command. npm installs the binary for your machine as a platform package, with no install script, so pnpm, bun and `--ignore-scripts` work too.

**From source**, with Go:

```sh
go install github.com/nearhost/nearhost/cmd/nearhost@latest
```

Nearhost needs Go 1.27.3 or later, the version the release binaries are built with. Go 1.21 to 1.27.2 downloads it first, unless `GOTOOLCHAIN` forbids it (some distributions' Go packages set it to `local`).

On Linux, allow the binary to bind ports 80 and 443. Run this again after each upgrade:

```sh
sudo setcap cap_net_bind_service=+ep "$(command -v nearhost)"
```

Installed from npm, `command -v nearhost` finds the Node launcher rather than the binary, so run the command `nearhost doctor` prints instead: it names the binary itself.

From Nearhost 0.9.0 the macOS binaries are signed with a Developer ID certificate and notarised by Apple, so Gatekeeper runs them however you download them.

Releases carry build provenance attestations, and a software bill of materials (`<archive>.sbom.json`) for each archive. To check a download, run `gh attestation verify <file> --repo nearhost/nearhost`.

### Updating

```sh
nearhost update
```

updates Nearhost the way you installed it: `brew upgrade`, `npm install -g`, `pnpm add -g`, `bun add -g`, `go install`, or the install script into the same directory. It shows the command and asks first; `--yes` skips the question, and `--check` only reports, exiting 1 when a newer release is out.

Nearhost also asks GitHub for the latest release at most once a day and prints a line when one is out. Set `NEARHOST_NO_UPDATE_CHECK=1` to turn that off.

## Quick start

Trust the local certificate authority. This asks for your password once:

```sh
nearhost trust
```

The local CA's key is in `~/Library/Application Support/nearhost/caddy` (macOS) or `~/.local/share/nearhost/caddy` (Linux). On either, an absolute `XDG_DATA_HOME` moves it to `$XDG_DATA_HOME/nearhost/caddy`. Anyone with the key can sign certificates your browser trusts, so keep it out of dotfiles repos and shared backups. Nearhost 0.7 and earlier kept it in `~/.config/nearhost/caddy` (or `$XDG_CONFIG_HOME/nearhost/caddy`). That copy is moved the next time `nearhost`, `nearhost up`, `nearhost trust` or `nearhost untrust` runs while no other Nearhost is running, and `nearhost doctor` warns if one is left behind. `nearhost untrust` removes the CA from the system trust store.

Add a project from its directory. Nearhost reads the ports from its `.env` and saves the routes to `~/.config/nearhost/config.yaml`. Nothing in the project is changed.

```text
$ cd /home/dev/code/myapp
$ nearhost add
Routes found for myapp (local:/home/dev/code/myapp):
  PORT  ROLE    LABEL
  5173  ignore  vite (VITE_PORT)
  5432  ignore  db (DB_PORT)
  8080  web     app (APP_PORT)

Added myapp on local at https://myapp.test
Start it with `nearhost up`.
```

When more than one port could be the web app, `add` asks which one it is. Pass `--web <port>` to choose up front.

Then start Nearhost and open `https://myapp.test`:

```sh
nearhost
```

`nearhost` opens the interface: your servers and projects, full screen. It starts every enabled project, shows each one's state, routes and log, and stops them all when you press `q`. Press `n` to add a project and `s` to add a server, `enter` to start or stop the selected project and `o` to open it in your browser. Adding a project walks through the server, the path (`tab` completes it, on a server too), the routes Nearhost found (`space` ticks a route to serve it, `←` and `→` change its role), and the name, in a box over the interface. With no projects yet it opens on the Nearhost mark and asks you to add one.

`/` filters the sidebar by name. `l` opens the selected project's log, or every log from Home, across the whole window: `tab` shows requests, events or errors, `/` searches, `enter` opens a request to show its headers, the body sent and the body that came back, and its route, `v` reveals its cookies, `c` copies it as a curl command, `r` sends it again (asking first unless it is a GET, HEAD or OPTIONS), and `G` follows new lines again. Home lists anything that needs you, such as a server that cannot connect, with how many attempts failed and when the next is, and how to fix it; there `r` retries the connection and `y` copies the `ssh` command that fixes access. On a server, `c` drops its connection and holds it closed, its projects waiting, until `c` connects it again. `?` lists every key. The mouse works too: drag to select and copy text, double-click to copy a word, and click an address to open it.

To show bodies and send requests again, the proxy keeps each request and its response in memory while Nearhost runs: bodies up to 64 KiB, the last thousand or so exchanges, never written to disk or to the log. `--no-capture` turns it off.

Everything else you would change in the config is there too. On a project, `tab` moves to its routes, where `space` switches a route between loopback and ignore and `w` makes it the web route; its advice shows below them, and `y` copies the suggested snippets. `e` edits a project, running discovery again, and `d` removes it. On a server, `r` reconnects it, `e` edits its host or name, and `d` removes it with its projects. While the local certificate authority is not trusted, Nearhost opens with a screen that explains the two password prompts and trusts it for you. When Nearhost needs your password, to update `/etc/hosts` or to trust the CA, it asks in a box over the interface; sudo keeps it for a few minutes, so you are rarely asked twice. Where sudo is set to ask every time, Nearhost asks in the terminal instead, and comes back to the interface afterwards.

For scripts and terminals without a screen, `nearhost up` serves the same projects headless until you press Ctrl-C.

### Nearhost's page

While Nearhost runs, a short page at https://localhost:6327 confirms it is up and shows its version. If something else holds port 6327, Nearhost starts without the page and says so. `--web-port <port>` moves the page, and `--web-port 0` turns it off.

### Share a project's setup

`nearhost export` writes what Nearhost serves for a project, which is its routes, hosts and main name, to a `.nearhostrc` in the project's directory. Commit it, and whoever runs `nearhost add` after cloning gets the same setup. `--dry-run` prints the file instead, and an existing one is replaced only with `--force`.

### Any domain, and more hosts

A project's main name is `<name>.test` unless you choose another. `add --alias` takes any domain, and `add --host` adds a further host to the web route; repeat it for more. Typing a name on the command line approves it.

```sh
nearhost add --alias app.example.test --host admin.example.test
```

### A project on another machine

Give the server's name and the project's path there. Any host that `ssh` reaches without prompting will do. When the server is new, `add` offers to add it:

```text
$ nearhost add --server mini ~/code/myapp
Server mini is not in the config. Add it? [Y/n]: y
SSH host for mini [mini]:
```

`~` is the home directory on the server, even when your shell has already expanded it to the one on your laptop. The files are read over SFTP, and nothing is installed on the server. Then start Nearhost as before:

```sh
nearhost
```

Servers are kept under `servers:` in the config, as a name and an `ssh_host`, if you would rather add them by hand.

### Presets

The first time you run `nearhost add` at a terminal, it offers to trust the official preset registry. You can also trust it, or any other public GitHub repository of presets, yourself:

```sh
nearhost preset registry add nearhost/presets
nearhost preset list
nearhost preset enable nearhost/compose
```

From then on, `add` suggests presets that suit the project and asks before enabling them.

### From a coding agent

A coding agent can add the project it is working in, whether it runs on this machine or on a server Nearhost is connected to. Install the `nearhost` skill with the [skills](https://skills.sh) CLI, which puts it where your agents look for skills: Claude Code, Codex, Cursor, Gemini CLI, GitHub Copilot and many more. Do it on each machine an agent runs on:

```sh
npx skills add nearhost/nearhost -g
```

`-g` installs it for your user rather than into the current project; `-a codex` and the like pick the agents. In Claude Code you can also install it as a plugin, with `/plugin install nearhost --marketplace nearhost/nearhost`.

[![skills.sh](https://skills.sh/b/nearhost/nearhost)](https://skills.sh/nearhost/nearhost)

Then ask the agent to add the project, with `/nearhost add` in Claude Code or `$nearhost add` in Codex. The agent proposes it, and Nearhost opens the add form over its interface with the server and path filled in, for you to save or dismiss. It notifies the terminal it runs in when the proposal arrives. `/nearhost status` tells the agent where a project is served and whether it is running.

Agents reach Nearhost on `~/.nearhost/<id>.sock`. Nearhost listens there on this machine, and forwards one there on each server over its own SSH connection. That directory is the only thing Nearhost creates on a server. A server whose sshd sets `AllowStreamLocalForwarding no` cannot take the forward, and `nearhost doctor` says so. An agent that runs commands in a sandbox needs to reach `~/.nearhost/*.sock`: approve the skill's command when the agent asks to run it outside the sandbox, which opens nothing else. The [documentation](https://nearhost.dev/docs/getting-started/use-a-coding-agent/) covers the settings, and what the broader ones open. Set `notifications: false` in the config to keep the terminal quiet.

### Useful commands

| Command | What it does |
| --- | --- |
| `nearhost` | Open the interface, which serves every enabled project |
| `nearhost add [path]` | Add a project, discovering its ports from its files; `--alias`, `--host`, `--server` and `--web` shape it |
| `nearhost ls` | List the configured projects and their routes |
| `nearhost up [project...]` | Serve the named projects, or every enabled one, without the interface |
| `nearhost export [project]` | Write the project's setup to a `.nearhostrc` to commit with it |
| `nearhost preset` | Manage presets and the registries you trust |
| `nearhost update` | Update Nearhost the way you installed it |
| `nearhost doctor` | Check ports, the certificate authority, the hosts block, server access and whether agents there reach Nearhost |
| `nearhost hosts clean` | Remove the Nearhost block from `/etc/hosts` |
| `nearhost trust` | Trust the local certificate authority; asks for your password |
| `nearhost untrust` | Remove the certificate authority from the trust store |

`nearhost --version` prints the version. Every command takes `--help`. The [documentation](https://nearhost.dev/docs/) covers routes and roles, the config file and troubleshooting in more depth.

## How it compares

Nearhost serves your own machines to your own browser. To show work to someone else, use a public tunnel service instead.

- **`ssh -L` by hand** gives you the same OpenSSH forwards, typed per port and retyped after every dropped connection, with no hostname and no certificate. Nearhost keeps one supervised SSH connection per server, adds and removes forwards while it runs, and puts a trusted `https://name.test` in front.
- **Editor port forwarding** gives you `localhost:port` URLs while the editor window stays open. Nearhost runs in a terminal, and each project keeps the same HTTPS name every day.
- **A VPN or mesh network** usually needs an agent on every machine and a dev server that listens beyond `localhost`. Nearhost installs nothing on the server, and the dev server keeps listening on `localhost`.
- **A public tunnel service** sends your traffic through someone else's relay to a public URL, usually behind an account. Nearhost has no relay, no account and no public URL.

## Development

You need Git and Go 1.27.3 or later (Go 1.21 and later download it if needed, unless `GOTOOLCHAIN` forbids it). Make is optional; each target is one Go command.

### Check out and build

```sh
git clone https://github.com/nearhost/nearhost.git
cd nearhost
make build        # go build -v -o nearhost ./cmd/nearhost
./nearhost version
```

A source build reports its version as `dev`.

### Run from source

Point Nearhost at scratch directories so a development build leaves your real setup alone: `XDG_CONFIG_HOME` keeps its config apart, `XDG_DATA_HOME` keeps its certificate authority apart, and `--no-hosts` leaves `/etc/hosts` as it is. Unprivileged ports avoid needing `setcap` on Linux:

```sh
export XDG_CONFIG_HOME="$PWD/.dev"
export XDG_DATA_HOME="$PWD/.dev/data"
./nearhost add --web 8080 ~/code/myapp
./nearhost up --no-hosts --https-port 18443 --http-port 18080 --web-port 16327
```

`./nearhost --no-hosts --https-port 18443 --http-port 18080 --web-port 16327` opens the interface with the same settings.

Without the hosts block, `myapp.test` does not resolve, so ask for it by address:

```sh
curl -k --resolve myapp.test:18443:127.0.0.1 https://myapp.test:18443/
```

The source build makes its own CA in `.dev/data`, apart from the installed Nearhost's. To drop `-k`, run `./nearhost trust` with the same variables set, which adds that dev CA to the system trust store. When you are done, run `./nearhost untrust` with them still set, so it removes the dev CA rather than the installed one.

### Test

```sh
make test         # go test ./...
make vet          # go vet ./...
```

The tests need no network, Docker or SSH server. SSH is exercised through a fake `ssh` in `internal/transport/testdata`.

CI also runs the official presets' fixtures against each build. To do the same locally, check out [nearhost/presets](https://github.com/nearhost/presets) beside this repository:

```sh
make build
make -C ../presets check NEARHOST="$PWD/nearhost"
```

### Layout

| Path | Contents |
| --- | --- |
| `cmd/nearhost` | The entry point |
| `internal/cli` | Commands, flags and prompts |
| `internal/config` | The config file and its validation |
| `internal/discovery` | Reading `.env` and Compose files to propose routes |
| `internal/preset`, `internal/registry` | The preset language and preset registries |
| `internal/engine` | Starting and stopping projects |
| `internal/transport` | SSH connections and port forwards |
| `internal/proxy` | The HTTPS proxy and local certificate authority |
| `internal/hosts` | The managed block in `/etc/hosts` |
| `internal/agentapi` | The API coding agents reach Nearhost on |
| `agents/nearhost` | The plugin and skill for coding agents |
| `npm` | The `@nearhost/cli` launcher, its README, and the scripts that build and publish the npm packages from a release |

### Contributing

Open a pull request against `main`. Titles are [Conventional Commits](https://www.conventionalcommits.org/), pull requests are squash merged, and [release-please](https://github.com/googleapis/release-please) cuts releases from the messages. The full guidance is in [CONTRIBUTING.md](https://github.com/nearhost/.github/blob/main/CONTRIBUTING.md).

## Licence

[MIT](LICENSE)
