---
name: nearhost
description: Serve the project you are working in at https://<name>.test with Nearhost, or say whether and where it is served. Use when the user runs /nearhost add or /nearhost status, or asks to add, serve or set up this project in Nearhost, or asks for its .test URL. Works on the laptop that runs Nearhost and on servers that Nearhost reaches over SSH.
argument-hint: "add [--web <port>] [--name <name>] | status [--all]"
allowed-tools: Bash(sh ${CLAUDE_SKILL_DIR}/scripts/nearhost.sh *)
---

# Nearhost

Nearhost runs on the user's laptop and serves projects, on the laptop or on servers it reaches over SSH, at `https://<name>.test`. You may be on the laptop or on one of those servers: you do not need to know which. The script `scripts/nearhost.sh`, next to this file, finds Nearhost and talks to it.

Run it as `sh <this skill's directory>/scripts/nearhost.sh <command>`; in Claude Code that is `sh ${CLAUDE_SKILL_DIR}/scripts/nearhost.sh <command>`. It prints JSON from Nearhost on success and one line saying what to do on failure.

The user asked for: $ARGUMENTS

If that is empty or reads as a placeholder, take `add` or `status` from what the user asked.

## add

1. Work out the dev server's port only if the project's own files say it plainly, such as `server.port` in `vite.config.ts` or `PORT=` in `.env`. Otherwise leave it to Nearhost, which reads them itself.
2. Run `sh .../nearhost.sh propose [--web <port>] [--name <name>] .` from the project's directory. Pass `--name` only when the user gave one.
3. If the JSON says `"state":"exists"`, the project is already in Nearhost: report its `url` and `state`, and stop.
4. Otherwise it printed an `id`. Tell the user: "Review it in Nearhost on <laptop>." Get `<laptop>` from `sh .../nearhost.sh socket`.
5. Run `sh .../nearhost.sh wait <id>`. While it prints `"state":"pending"`, run it again. Each run waits up to 50 seconds.
6. On `"state":"accepted"`, report the project's `url`. You may run `sh .../nearhost.sh status` to check it is running.
7. Exit status 6 means the user declined it: say so, with the `reason` if there is one. Exit status 7 means nobody answered within 10 minutes: say so; the user can run `/nearhost add` again.

The user decides in Nearhost which ports to serve and what to call the project. Do not edit Nearhost's config file yourself.

## status

Run `sh .../nearhost.sh status` for the project holding this directory, or `status --all` for every project on this machine. Report each project's `url`, `state` and any `error`. Exit status 1 with `no project holds` means this directory is not in Nearhost yet: say so, and offer `/nearhost add`.

## When the script fails

- **Exit status 2 from `wait`**: Nearhost stopped while the proposal waited, so the user never answered it. Say so and stop. Do not add the project any other way: the user reviews every add.
- **Exit status 2 from `propose` or `status`**, no Nearhost answers. Pass on what the script printed.
  - On a server, it says what to do on the laptop: start `nearhost` and connect this server, or add the server first.
  - On the laptop, if it says the `nearhost` command is here and nothing was proposed yet: you may run `nearhost add <path>` yourself, since the user asked for the add at this machine. If that asks which port is the web port, ask the user, then run `nearhost add --web <port> <path>`. Then tell them to start `nearhost` to serve it.
- **Exit status 3**: Nearhost is running as `nearhost up`, which cannot show proposals. Tell the user to open `nearhost` on the laptop and try again.
- **Exit status 4**: Nearhost answers from more than one laptop. Ask the user which, then set `NEARHOST_SOCKET` to that socket, as the script printed, for each later run.
- **Exit status 5**: this skill and Nearhost speak different versions of the API. Pass on which one to update.
- **Exit status 1**: Nearhost refused. Pass on its `error`.
- **A sandbox denies the connection**: the agent's sandbox blocks Unix sockets. Tell the user that Nearhost needs access to `~/.nearhost/*.sock`, and see https://nearhost.dev/docs/ for how to allow it.
