---
name: nearhost
description: Serve the project you are working in at its own domain (https://<name>.test by default) with Nearhost, or say whether and where it is served. Use when the user runs /nearhost add, /nearhost status or /nearhost doctor, or asks to add, serve or set up this project in Nearhost, or asks for its URL, or when a project isn't served as expected. Works on the laptop that runs Nearhost and on servers that Nearhost reaches over SSH.
argument-hint: "add [--web <port>] [--name <name>] | status [--all] | doctor"
allowed-tools: Bash(sh ${CLAUDE_SKILL_DIR}/scripts/nearhost.sh *)
---

# Nearhost

Nearhost runs on the user's laptop and serves projects, on the laptop or on servers it reaches over SSH, at each project's own domain, `https://<name>.test` by default. You may be on the laptop or on one of those servers: you do not need to know which. The script `scripts/nearhost.sh`, next to this file, finds Nearhost and talks to it.

Run it as `sh <this skill's directory>/scripts/nearhost.sh <command>`; in Claude Code that is `sh ${CLAUDE_SKILL_DIR}/scripts/nearhost.sh <command>`. It prints JSON from Nearhost on success and one line saying what to do on failure.

The user asked for: $ARGUMENTS

If that is empty or reads as a placeholder, take `add`, `status` or `doctor` from what the user asked.

## add

1. Work out the dev server's port only if the project's own files say it plainly, such as `server.port` in `vite.config.ts` or `PORT=` in `.env`. Otherwise leave it to Nearhost, which reads them itself.
2. Run `sh .../nearhost.sh propose [--web <port>] [--name <name>] .` from the project's directory. Pass `--name` only when the user gave one.
3. If the JSON says `"state":"exists"`, the project is already in Nearhost: report its `url` and `state`, and stop.
4. Otherwise it printed an `id`. Tell the user: "Review it in Nearhost on <laptop>." Get `<laptop>` from `sh .../nearhost.sh socket`.
5. Run `sh .../nearhost.sh wait <id>`. While it prints `"state":"pending"`, run it again. Each run waits up to 50 seconds.
6. On `"state":"accepted"`, report the project's `url`. Run `sh .../nearhost.sh status` a few seconds apart while its `state` is `starting`, for 30 seconds at most. Then run `sh .../nearhost.sh doctor .` whatever the state is: it reports a project that is stopped or degraded itself. If that exits with status 5, this Nearhost has no doctor: skip it without saying so. When it finds errors or warnings, offer to work through them as in `doctor`.
7. Exit status 6 means the user declined it: say so, with the `reason` if there is one. Exit status 7 means nobody answered within 10 minutes: say so; the user can run `/nearhost add` again.

The user decides in Nearhost which ports to serve and what to call the project. Do not edit Nearhost's config file yourself.

When a project works in Nearhost and has no `.nearhostrc`, suggest the user run `nearhost export` to commit its setup. Never run it unasked.

## status

Run `sh .../nearhost.sh status` for the project holding this directory, or `status --all` for every project on this machine. Report each project's `url`, any other `urls`, `state` and any `error`. Exit status 1 with `no project holds` means this directory is not in Nearhost yet: say so, and offer `/nearhost add`.

## doctor

Use this when a project isn't served as the user expects.

1. Run `sh .../nearhost.sh doctor .` and read `findings`. Work through `error` findings, then `warning`. Mention `info` only when it bears on what the user asked.
2. For a fix of kind `project`: show the user the change, the `snippet` at `file`:`line`, and make it in the project's files with their agreement. Never make it in Nearhost's config. When the setup is more than Nearhost can infer (several hosts, patterns, `localhost`, Docker callbacks), write or edit `.nearhostrc`. `nearhost export` writes the current setup, to start from.
3. After editing `.nearhostrc`, and for the findings `nearhostrc.pending` and `nearhostrc.not-applied`, run `sh .../nearhost.sh reload .`.
   - On `"state":"pending"` (status 202), tell the user: "Approve it in Nearhost on <laptop>." Then run `sh .../nearhost.sh wait <id>` as in add.
   - Exit status 6 means the user declined it.
   - Exit status 8 means the file changed again, or the change stopped waiting: run `reload .` again.
   - Exit status 3 means Nearhost runs as `nearhost up`: ask the user to open `nearhost`.
4. Any other fix of kind `nearhost` is something the user does in Nearhost, and a reload changes nothing for it. Pass its `text` on to the user, or offer to write the port or host into `.nearhostrc` instead and then reload as above.
5. For a fix of kind `manual`, tell the user plainly what its `text` says. The user does those.
6. Run `doctor .` again. Stop when no error or warning is left that you can fix, then say what is left.

Do not edit Nearhost's config file yourself.

## When the script fails

- **Exit status 2 from `wait`**: Nearhost stopped while the proposal waited, so the user never answered it. Say so and stop. Do not add the project any other way: the user reviews every add.
- **Exit status 2 from `propose` or `status`**, no Nearhost answers. Pass on what the script printed.
  - On a server, it says what to do on the laptop: start `nearhost` and connect this server, or add the server first.
  - On the laptop, if it says the `nearhost` command is here and nothing was proposed yet: you may run `nearhost add <path>` yourself, since the user asked for the add at this machine. If that asks which port is the web port, ask the user, then run `nearhost add --web <port> <path>`. Then tell them to start `nearhost` to serve it.
- **Exit status 2 and the script printed `curl is needed`**: this machine has no curl, so nothing was asked of Nearhost. Tell the user to install curl. Do not run `nearhost add` instead.
- **Exit status 3**: Nearhost is running as `nearhost up`, which cannot show proposals. Tell the user to open `nearhost` on the laptop and try again.
- **Exit status 4**: Nearhost answers from more than one laptop. Ask the user which, then set `NEARHOST_SOCKET` to that socket, as the script printed, for each later run.
- **Exit status 5**: this skill and Nearhost speak different versions of the API, or this Nearhost is too old for `doctor` or `reload`. Pass on which one to update.
- **Exit status 8 from `wait`**: the change of `.nearhostrc` that waited no longer does, as a newer one replaced it or the file changed back. Run `reload .` again.
- **Exit status 1**: Nearhost refused. Pass on its `error`.
- **A sandbox denies the connection**: your sandbox blocks Unix sockets. If you can ask to run that one command outside the sandbox, do so: approving it opens nothing else. Do not suggest settings that open every socket, such as `allowAllUnixSockets` or full network access, unless the user asks; they also expose sockets like Docker's. Otherwise tell the user that Nearhost needs `~/.nearhost/*.sock`, and point them to https://nearhost.dev/docs/getting-started/use-a-coding-agent/.
