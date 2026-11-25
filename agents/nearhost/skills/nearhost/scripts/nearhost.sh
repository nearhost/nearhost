#!/bin/sh
# nearhost.sh is how the nearhost skill talks to Nearhost: the agent API on
# ~/.nearhost/<id>.sock, which Nearhost serves on the laptop it runs on and
# forwards to each server it is connected to. It needs only sh and curl.
#
#   nearhost.sh socket                                  find Nearhost
#   nearhost.sh status [--all] [path]                   the project holding path
#   nearhost.sh propose [--web N] [--name X] [path]     ask to add the project
#   nearhost.sh wait <id>                               wait up to 50s for the answer
#   nearhost.sh doctor [path]                           check how the project is served
#   nearhost.sh reload [path]                           make Nearhost re-read .nearhostrc
#   nearhost.sh passthrough <port> [--label X] [--on H] open a server's port on the laptop
#   nearhost.sh passthrough-wait <id> [--on H]          wait up to 50s for the answer
#   nearhost.sh passthrough-close --session <id> | <id> [--on H]
#                                                       close a session's passthroughs, or one
#   nearhost.sh open <path> [--label X]                 open a document in the user's browser
#   nearhost.sh open-wait <id>                          wait up to 50s for the answer
#
# It prints the API's JSON. Exit status: 0 done, 1 refused or failed,
# 2 no Nearhost reachable, no curl to reach it with or no token it accepts, 3 Nearhost has no page or window to ask the user in, 4 several
# laptops answer, 5 API mismatch or a call this Nearhost lacks, 6 declined
# by the user, or refused as the user declined one moments ago, 7 expired, 8 superseded: the change waited on no longer waits.
# passthrough-close always exits 0: it runs when a session ends.
#
# --on H runs the command on H, an SSH host this machine reaches, where the
# dev server runs: the script sends itself there and Nearhost lists the
# passthrough under that server. When SSH cannot reach H, it exits 2.

set -u

API=1

die() {
	code=$1
	shift
	printf '%s\n' "$*" >&2
	# DOCUMENT is set by open, which must leave the agent with the path to
	# give the user when Nearhost cannot show it.
	if [ -n "${DOCUMENT:-}" ]; then
		case $code in
		2) if [ -n "${DENIED:-}" ]; then
			printf '%s\n' "Nothing was opened. Tell the user the document is at $DOCUMENT." >&2
		elif [ "${1#curl}" = "$1" ]; then
			printf '%s\n' "Nearhost isn't running or connected, so nothing was opened. Tell the user the document is at $DOCUMENT." >&2
		fi ;;
		3 | 5) printf '%s\n' "Nothing was opened. Tell the user the document is at $DOCUMENT." >&2 ;;
		esac
	fi
	# SOFT is set by passthrough-close, which always exits 0.
	[ -n "${SOFT:-}" ] && exit 0
	exit "$code"
}

# field NAME JSON prints the last top-level NAME in JSON. The API writes
# objects with their keys sorted and nests only the project, which sorts
# before the fields read here, so the last match is the top-level one.
field() {
	printf '%s' "$2" | sed -n "s/.*\"$1\":\"\{0,1\}\([^\",}]*\)\"\{0,1\}.*/\1/p"
}

# text_field NAME JSON prints the first string NAME in JSON in full, where
# field stops at the first comma. Of the escapes, \n, \t and \r become a
# space, as do \u2028 and \u2029; \u003c, \u003e and \u0026 (the ones Go's
# JSON encoder writes) become <, > and &. Any other escape drops its
# backslash and keeps the rest.
text_field() {
	printf '%s' "$2" | NAME=$1 awk '{
		key = "\"" ENVIRON["NAME"] "\":\""
		i = index($0, key)
		if (!i)
			next
		s = substr($0, i + length(key))
		out = ""
		while (s != "") {
			c = substr(s, 1, 1)
			if (c == "\"")
				break
			if (c == "\\") {
				s = substr(s, 2)
				c = substr(s, 1, 1)
				if (c == "n" || c == "t" || c == "r")
					c = " "
				else if (c == "u") {
					u = tolower(substr(s, 2, 4))
					if (u == "003c" || u == "003e" || u == "0026" || u == "2028" || u == "2029") {
						c = (u == "003c") ? "<" : (u == "003e") ? ">" : (u == "0026") ? "&" : " "
						s = substr(s, 5)
					}
				}
			}
			out = out c
			s = substr(s, 2)
		}
		print out
		exit
	}'
}

# call SOCKET SECONDS CURL-ARGS... sets STATUS and BODY, or fails when
# nothing answers on SOCKET. A Nearhost on Windows answers only requests
# with its token, which it leaves beside the socket as a header line: curl
# reads it from the file, so the token never shows on a command line.
call() {
	sock=$1
	limit=$2
	shift 2
	headers=${sock%.sock}.token
	if [ -r "$headers" ]; then
		set -- -H "@$headers" "$@"
	fi
	out=$(curl -sS --unix-socket "$sock" --max-time "$limit" -w '\n%{http_code}' "$@" 2>/dev/null) || return 1
	STATUS=$(printf '%s\n' "$out" | tail -n 1)
	BODY=$(printf '%s\n' "$out" | sed '$d')
}

# no_nearhost says what to do when no Nearhost answers here.
no_nearhost() {
	# A session's close run by --on: with no Nearhost here, its passthroughs
	# ended with that Nearhost's connection, so there is nothing to close.
	[ -n "${NONE_OK:-}" ] && exit 0
	if [ -n "${SSH_CONNECTION:-}" ]; then
		host=$(hostname 2>/dev/null | cut -d. -f1)
		if [ -n "${PASSPORT:-}" ]; then
			die 2 "Nearhost isn't connected to this server. On your laptop, select ${host:-<server>} in Nearhost and press c, then ask again. Or forward it yourself: ssh -N -L $PASSPORT:localhost:$PASSPORT ${host:-<server>}"
		fi
		# Nearhost forwards its socket only while it holds a connection to
		# the server, which it makes when one of the server's projects starts
		# or when the user connects it.
		die 2 "No Nearhost reaches this server. On your laptop, start nearhost there and connect this server: $(connect_steps "${host:-<server>}") If this server is not in Nearhost yet, run this on the laptop instead: nearhost add --server ${host:-<server>} ${TARGET:-$PWD}"
	fi
	if command -v nearhost >/dev/null 2>&1; then
		die 2 "Nearhost is not running on this machine. Run: nearhost add <path>, then start nearhost to serve it."
	fi
	# No ssh, and no nearhost command: this is a server an agent reaches some
	# other way, such as a remote session of its own, or a machine without
	# Nearhost. The script cannot tell which, so it says both.
	host=$(hostname 2>/dev/null | cut -d. -f1)
	die 2 "No Nearhost reaches this machine. If you reach it from a laptop that runs Nearhost, connect it there: start nearhost, $(connect_steps "${host:-<server>}") Then ask again. If ${host:-this machine} is not in Nearhost yet, run this on the laptop: nearhost add --server ${host:-<server>} ${TARGET:-$PWD}. To serve projects on this machine itself, install Nearhost here: https://nearhost.dev/docs/"
}

# connect_steps says how to connect the server called $1 in Nearhost: from
# its row, or from the first screen while there is nothing to serve.
connect_steps() {
	printf 'select %s and press c, or, while Nearhost has nothing to serve, press c on its first screen and choose %s.' "$1" "$1"
}

# no_token exits 2 with what a Nearhost that wants a token said when the
# request had none it accepts: the error in BODY, a 401's.
no_token() {
	DENIED=1
	msg=$(text_field error "$BODY")
	die 2 "${msg:-this request has no valid Nearhost token: update the Nearhost skill on this machine}"
}

# find_socket sets SOCK and HELLO, the answer to GET /v1, or exits.
find_socket() {
	command -v curl >/dev/null 2>&1 || die 2 "curl is needed to reach Nearhost and is not installed here. Install curl, then run this again."
	if [ -n "${NEARHOST_SOCKET:-}" ]; then
		if ! call "$NEARHOST_SOCKET" 2 http://nearhost/v1; then
			die 2 "Nothing answers on NEARHOST_SOCKET ($NEARHOST_SOCKET). Unset it to look in ~/.nearhost."
		fi
		[ "$STATUS" = 401 ] && no_token
		[ "$STATUS" = 200 ] || die 2 "Nothing answers on NEARHOST_SOCKET ($NEARHOST_SOCKET). Unset it to look in ~/.nearhost."
		SOCK=$NEARHOST_SOCKET
		HELLO=$BODY
	else
		found=0
		list=""
		names=""
		# denied holds the answer of a socket that wants a token this
		# machine lacks. It is the one to report only when no other
		# socket answers.
		denied=""
		for s in "$HOME"/.nearhost/*.sock; do
			[ -S "$s" ] || continue
			# Sockets that do not answer are left by runs that have ended.
			call "$s" 2 http://nearhost/v1 || continue
			if [ "$STATUS" = 401 ]; then
				denied=$BODY
				continue
			fi
			[ "$STATUS" = 200 ] || continue
			found=$((found + 1))
			list="$list  NEARHOST_SOCKET=$s  ($(field laptop "$BODY"))
"
			names="$names  $(field laptop "$BODY")
"
			SOCK=$s
			HELLO=$BODY
		done
		case $found in
		0)
			if [ -n "$denied" ]; then
				BODY=$denied
				no_token
			fi
			no_nearhost
			;;
		1) ;;
		*)
			if [ -n "${REMOTE:-}" ]; then
				# NEARHOST_SOCKET set on the agent's machine does not cross SSH.
				here=$(hostname 2>/dev/null | cut -d. -f1)
				printf 'Nearhost answers on %s from %s laptops, and --on cannot choose between them. Ask the user which laptop to use, and to drop %s in Nearhost on the others:\n%s' \
					"${here:-this server}" "$found" "${here:-this server}" "$names" >&2
			else
				printf 'Nearhost answers here from %s laptops. Ask which one, then set NEARHOST_SOCKET:\n%s' "$found" "$list" >&2
			fi
			[ -n "${SOFT:-}" ] && exit 0
			exit 4
			;;
		esac
	fi
	api=$(field api "$HELLO")
	if [ "$api" != "$API" ]; then
		laptop=$(field laptop "$HELLO")
		if [ "${api:-0}" -gt "$API" ] 2>/dev/null; then
			die 5 "Nearhost on $laptop speaks API $api and this skill speaks $API: update the nearhost plugin."
		fi
		die 5 "Nearhost on $laptop speaks API ${api:-?} and this skill speaks $API: update Nearhost on $laptop."
	fi
}

# need CAP exits 5 unless this Nearhost lists CAP in its capabilities. An
# older Nearhost has no such list, which counts as lacking every one.
need() {
	caps=$(printf '%s' "$HELLO" | sed -n 's/.*"capabilities":\[\([^]]*\)\].*/\1/p')
	case $caps in
	*\"$1\"*) ;;
	*) die 5 "Nearhost on $(field laptop "$HELLO") doesn't support $1 yet: update Nearhost there." ;;
	esac
}

# absolute PATH prints PATH as an absolute directory, as the shell sees it:
# a symlink on the way stays.
absolute() {
	(cd "$1" 2>/dev/null && pwd) || die 1 "$1 is not a directory here."
}

# resolved PATH prints PATH as an absolute directory with its symlinks
# resolved. The project may be in Nearhost's config either way.
resolved() {
	(cd "$1" 2>/dev/null && pwd -P)
}

# quote STRING prints STRING as a JSON string. awk reads it from the
# environment, which leaves backslashes as they are, and escapes it a
# character at a time: awks disagree on backslashes in gsub's replacement,
# and mawk 1.3.4 20200120, Debian 12's and Ubuntu 22.04's, gets them wrong.
quote() {
	QUOTE=$1 awk 'BEGIN {
		s = ENVIRON["QUOTE"]
		out = ""
		for (i = 1; i <= length(s); i++) {
			c = substr(s, i, 1)
			if (c == "\\") c = "\\\\"
			else if (c == "\"") c = "\\\""
			else if (c == "\t") c = "\\t"
			else if (c == "\n") c = "\\n"
			else if (c == "\r") c = "\\r"
			out = out c
		}
		printf "\"%s\"", out
	}'
}

# approve_link prints on stderr where the user can answer the request in
# BODY, a 202, on Nearhost's page, when the API gave the link. For a proposal
# to add a project, which the page cannot accept, it gives the advice and
# the link to decline it. It returns.
approve_link() {
	url=$(text_field approve_url "$BODY")
	[ -n "$url" ] || return 0
	advice=""
	[ "${1:-}" = add ] && advice=$(text_field advice "$BODY")
	if [ -n "$advice" ]; then
		printf '%s Or decline it at %s\n' "$advice" "$url" >&2
	else
		printf 'Answer in Nearhost on %s, or open %s\n' "$(field laptop "$HELLO")" "$url" >&2
	fi
}

# answer prints BODY and exits for STATUS: 0 for success, otherwise with the
# API's error. It gives the link to answer a 202 first, as approve_link does
# for KIND.
answer() {
	printf '%s\n' "$BODY"
	[ "$STATUS" = 202 ] && approve_link "${1:-}"
	case $STATUS in
	2??) exit 0 ;;
	409) exit 3 ;;
	*) exit 1 ;;
	esac
}

# held_back exits 6 when Nearhost refused the request because the user said
# No to one of its KIND from this machine moments ago, printing BODY and
# saying when the agent may ask again. It never asks again by itself.
# Otherwise it returns.
held_back() {
	[ "$STATUS" = 429 ] || return 0
	n=$(text_field error "$BODY" | sed -n 's/^refused; ask again in \([0-9][0-9]*\) s$/\1/p')
	[ -n "$n" ] || return 0
	printf '%s\n' "$BODY"
	if [ -n "${DOCUMENT:-}" ]; then
		said_no "The user said No to a $1 from this machine in Nearhost, so nothing was opened." "$n" " Tell the user the document is at $DOCUMENT."
	fi
	said_no "The user said No to a $1 from this machine in Nearhost." "$n"
}

# held exits 6 for a request in BODY that Nearhost ended without asking,
# as the user said No to another of its KIND from this machine, saying when
# the agent may ask again. It returns for any other.
held() {
	[ "$(field held "$BODY")" = true ] || return 0
	said_no "The user said No to another $1 from this machine in Nearhost, so this one was not asked." "$(field retry_after "$BODY")"
}

# said_no exits 6 saying WHAT, that the agent must not ask again by itself,
# and when it may if the user asks, which is after N seconds, then AFTER.
said_no() {
	if [ "${2:-0}" -gt 0 ] 2>/dev/null; then
		printf '%s\n' "$1 Do not ask again on your own; if the user asks you to, ask again in $2 seconds or later.${3:-}" >&2
	else
		printf '%s\n' "$1 Do not ask again on your own; if the user asks you to, you may ask again now.${3:-}" >&2
	fi
	exit 6
}

# requester prints who is asking: claude-code inside Claude Code, otherwise
# agent. Codex documents no variable to tell it by, so it sends agent. Run
# by --on, it is the requester the agent's machine sent.
requester() {
	if [ -n "${REMOTE:-}" ]; then
		printf '%s' "$REMOTE_REQUESTER"
	elif [ "${CLAUDECODE:-}" = 1 ]; then
		printf 'claude-code'
	else
		printf 'agent'
	fi
}

# session prints the agent's session ID: Claude Code's, or, run by --on, the
# one the agent's machine sent.
session() {
	if [ -n "${REMOTE:-}" ]; then
		printf '%s' "$REMOTE_SESSION"
	else
		printf '%s' "${CLAUDE_CODE_SESSION_ID:-}"
	fi
}

# sq STRING prints STRING single-quoted for a POSIX shell, each ' in it
# written as '\'', so sh -s on the other end of ssh reads it byte for byte.
sq() {
	rest=$1
	done_=""
	while :; do
		case $rest in
		*\'*)
			done_="$done_${rest%%\'*}'\\''"
			rest=${rest#*\'}
			;;
		*) break ;;
		esac
	done
	printf "'%s%s'" "$done_" "$rest"
}

# good_host HOST succeeds when HOST is an SSH host name that ssh cannot read
# as an option: letters, digits, dot, underscore, @ and dash, not starting
# with a dash.
good_host() {
	case $1 in
	'' | -* | *[!A-Za-z0-9._@-]*) return 1 ;;
	esac
}

# check_host HOST exits 1 unless HOST is a good_host.
check_host() {
	good_host "$1" || die 1 "--on needs the SSH host the dev server runs on, such as: --on apollo"
	[ -z "${REMOTE:-}" ] || die 1 "--on cannot be used on a host reached with --on"
}

# run_on HOST CMD ARGS... runs this script's CMD ARGS on HOST over SSH, with
# the requester and session from here, and exits with its status. Its
# output passes through, but for an SSH failure, which exits 2. When CMD
# opened a passthrough there, or asked for one, the session's marker here
# gains HOST, so the session's end closes it.
run_on() {
	host=$1
	shift
	check_host "$host"
	[ -f "$0" ] && [ -r "$0" ] || die 1 "--on needs this script as a file to send, not $0"
	ssh=$(command -v ssh 2>/dev/null) || ssh=""
	[ -n "$ssh" ] || die 2 "ssh is needed for --on and is not installed here."
	# The marker names the ssh that opened the passthrough, to close it the
	# same way; a path it can run from anywhere.
	case $ssh in
	/*) ;;
	*) die 2 "ssh is needed for --on and is not installed here." ;;
	esac
	# sshd runs the command through the user's login shell, which may not be
	# POSIX (fish, csh), so it is the constant sh -s and nothing else. The
	# arguments go on stdin ahead of the script, where sh reads them. ssh's
	# stderr is caught to tell its own failure apart, and its stdout to see
	# what the command did; both are passed on.
	rc=0
	out=$(
		rc=0
		{ err=$(remote_input "$@" 3>&- | "$ssh" -o BatchMode=yes -o ConnectTimeout=5 -- "$host" 'sh -s' 2>&1 1>&3 3>&-) || rc=$?; } 3>&1
		if [ "$rc" -eq 255 ]; then
			first=$(printf '%s\n' "$err" | sed -n '1p')
			die 2 "SSH could not reach $host: ${first:-ssh exited with status 255}"
		fi
		[ -n "$err" ] && printf '%s\n' "$err" >&2
		exit "$rc"
	) || rc=$?
	[ -n "$out" ] && printf '%s\n' "$out"
	if [ "$rc" -eq 0 ]; then
		# As here: a passthrough that opened, or one the user is asked about,
		# whose answer, a 202's, has no state.
		case $1:$(field state "$out") in
		passthrough:open | passthrough: | passthrough-wait:open)
			SESSION=$(session)
			save_session "ssh $ssh $host"
			;;
		esac
	fi
	exit "$rc"
}

# remote_input ARGS... prints what run_on sends to sh -s: a line setting the
# arguments to --remote, the requester, the session and ARGS, each one
# single-quoted, then this script.
remote_input() {
	printf 'set -- --remote %s %s' "$(sq "$(requester)")" "$(sq "$(session)")"
	for arg in "$@"; do
		printf ' %s' "$(sq "$arg")"
	done
	printf '\n'
	cat "$0"
}

# state_dir prints where the skill keeps tokens and session markers.
state_dir() {
	printf '%s/nearhost' "${XDG_STATE_HOME:-$HOME/.local/state}"
}

# plain_word WORD succeeds when WORD is safe in a file name and a URL: letters,
# digits, dot, underscore and dash, not starting with a dot.
plain_word() {
	case $1 in
	'' | .* | *[!A-Za-z0-9._-]*) return 1 ;;
	esac
}

# private_dir DIR... makes each directory, and gives it mode 0700 even when
# it existed already. It fails when a directory cannot be made or chmod'd.
private_dir() {
	for d in "$@"; do
		if [ ! -d "$d" ]; then
			(umask 077 && mkdir -p "$d") || return 1
		fi
		chmod 700 "$d" || return 1
	done
}

# private_file FILE TEXT writes TEXT and a newline to FILE with mode 0600.
private_file() {
	rm -f "$1"
	(umask 077 && printf '%s\n' "$2" >"$1")
}

# token_file prints the path of this requester's token for this laptop.
token_file() {
	lid=$(field id "$HELLO")
	plain_word "$lid" || return 1
	printf '%s/tokens/%s/%s' "$(state_dir)" "$lid" "$REQUESTER"
}

# doc_token_file prints the path of this requester's documents token for
# this laptop, next to the passthrough token and apart from it.
doc_token_file() {
	dtf=$(token_file) || return 1
	printf '%s.documents' "$dtf"
}

# save_token [FILE] stores the token in BODY, if it has one, in FILE or by
# default in the passthrough token file. An agent that can't be told apart
# from others never keeps one.
save_token() {
	[ "$REQUESTER" = agent ] && return 0
	tok=$(field token "$BODY")
	case $tok in
	'' | *[!A-Za-z0-9_-]*) return 0 ;;
	esac
	if [ $# -gt 0 ]; then
		tf=$1
	else
		tf=$(token_file) || return 0
	fi
	private_dir "$(state_dir)" "$(state_dir)/tokens" "$(dirname "$tf")" || return 0
	private_file "$tf" "$tok"
}

# show_body prints BODY without the one-time token, which belongs in the
# token file and never in what an agent reads.
show_body() {
	printf '%s\n' "$BODY" | sed 's/"token":"[^"]*",\{0,1\}//; s/,}/}/'
}

# save_session [TARGET] notes that this session has a passthrough open, or
# asked for, at TARGET, so that passthrough-close --session can find it
# without looking. The marker has a line per target: the socket of a
# Nearhost here, SOCK by default, or "ssh <ssh> <host>" for one opened on
# host with --on. A target already in it is not added again.
save_session() {
	# Run by --on, the session ends on the agent's machine, not here.
	[ -z "${REMOTE:-}" ] || return 0
	plain_word "${SESSION:-}" || return 0
	target=${1:-$SOCK}
	private_dir "$(state_dir)" "$(state_dir)/sessions" || return 0
	sm="$(state_dir)/sessions/$SESSION"
	if [ -f "$sm" ]; then
		grep -Fqx -- "$target" "$sm" 2>/dev/null && return 0
		printf '%s\n' "$target" >>"$sm"
	else
		private_file "$sm" "$target"
	fi
}

# close_socket SOCK SESSION closes SESSION's passthroughs on the Nearhost at
# SOCK, and fails unless that Nearhost confirms it. A socket that is gone
# succeeds: that Nearhost is not running, and its passthroughs ended with it.
# One from a drive letter, as nearhost.ps1 writes it on Windows, is only
# looked for: Git Bash, which runs the hook there, may not see a Windows
# socket as one.
close_socket() {
	case $1 in
	[A-Za-z]:[\\/]*) [ -e "$1" ] || return 0 ;;
	*) [ -S "$1" ] || return 0 ;;
	esac
	command -v curl >/dev/null 2>&1 || return 1
	call "$1" 1 -X DELETE "http://nearhost/v1/passthroughs?session=$2" && [ "$STATUS" = 204 ]
}

# runnable PATH prints PATH as this shell runs it. One from a drive letter,
# as nearhost.ps1 writes C:\Windows\System32\OpenSSH\ssh.exe, has no / for
# Git Bash, which runs the hook on Windows, so Git Bash would look for it on
# PATH: it gets its Unix form from cygpath, or its \ turned to /. Spaces in
# it stay; the caller keeps it one word.
runnable() {
	case $1 in
	[A-Za-z]:[\\/]*)
		if command -v cygpath >/dev/null 2>&1; then
			up=$(cygpath -u "$1" 2>/dev/null) || up=""
			if [ -n "$up" ]; then
				printf '%s' "$up"
				return
			fi
		fi
		printf '%s\n' "$1" | sed 's|\\|/|g'
		;;
	*) printf '%s' "$1" ;;
	esac
}

# close_ssh SSH HOST SESSION closes SESSION's passthroughs on HOST, running
# this script there with SSH, and fails unless it confirms that. Sent TERM,
# it ends the ssh too.
close_ssh() {
	cs=""
	trap 'if [ -n "$cs" ]; then kill "$cs" 2>/dev/null; fi; exit 1' TERM
	ssh_bin=$(runnable "$1")
	remote_input passthrough-close --session "$3" | "$ssh_bin" -o BatchMode=yes -o ConnectTimeout=2 -- "$2" 'sh -s' &
	cs=$!
	wait "$cs"
}

# close_session SESSION closes SESSION's passthroughs at every target in its
# marker at once, cutting off what has not finished after 2.5 seconds, and
# keeps in the marker only the targets that did not close.
close_session() {
	marker="$(state_dir)/sessions/$1"
	[ -f "$marker" ] || return 0
	# plan holds a word per line of the marker: the pid closing it, or - for
	# a line that names no target and is dropped.
	plan=""
	running=""
	while IFS= read -r line || [ -n "$line" ]; do
		job=-
		case $line in
		/* | [A-Za-z]:[\\/]*)
			(close_socket "$line" "$1") </dev/null >/dev/null 2>&1 &
			job=$!
			;;
		"ssh "*)
			rest=${line#ssh }
			host=${rest##* }
			bin=${rest% *}
			if [ "$bin" != "$rest" ] && good_host "$host"; then
				# An absolute path, or on Windows, where Git Bash runs the
				# hook, one with a drive letter as nearhost.ps1 writes it.
				case $bin in
				/* | [A-Za-z]:[\\/]*)
					if [ -f "$0" ] && [ -r "$0" ]; then
						(close_ssh "$bin" "$host" "$1") </dev/null >/dev/null 2>&1 &
					else
						(exit 1) &
					fi
					job=$!
					;;
				esac
			fi
			;;
		esac
		plan="$plan $job"
		[ "$job" = - ] || running="$running $job"
	done <"$marker"
	if [ -n "$running" ]; then
		# The hook has 3 seconds. A sleep that takes no fraction fails at
		# once, and the cut-off falls back to 2 seconds.
		# Sent TERM, it ends the sleep it waits on: the one $! names once it
		# differs from the $! it started with. TERM before the trap is set
		# ends it before any sleep starts. KILL, as a sleep just forked may
		# still catch TERM with this shell's trap, and lose it as it starts.
		(
			was=$!
			trap 'if [ "$!" != "$was" ]; then kill -s KILL "$!" 2>/dev/null; fi; exit 0' TERM
			sleep 2.5 2>/dev/null &
			wd=$!
			if ! wait "$wd"; then
				sleep 2 &
				wd=$!
				wait "$wd"
			fi
			# shellcheck disable=SC2086 # running is a list of pids.
			kill $running 2>/dev/null
		) </dev/null >/dev/null 2>&1 &
		watchdog=$!
	fi
	keep=""
	# shellcheck disable=SC2086 # plan is a list of words.
	set -- $plan
	while IFS= read -r line || [ -n "$line" ]; do
		[ $# -gt 0 ] || break
		job=$1
		shift
		[ "$job" = - ] && continue
		wait "$job" && continue
		keep="$keep$line
"
	done <"$marker"
	if [ -n "$running" ]; then
		kill "$watchdog" 2>/dev/null
		# dash reports a watchdog ended before its trap was set.
		wait "$watchdog" 2>/dev/null
	fi
	if [ -n "$keep" ]; then
		private_file "$marker" "${keep%?}"
	else
		rm -f "$marker"
	fi
}

# pass_find finds Nearhost and checks that it has passthroughs. PORT is the
# port asked for, for the way to forward it by hand.
pass_find() {
	find_socket
	case $(printf '%s' "$HELLO" | sed -n 's/.*"capabilities":\[\([^]]*\)\].*/\1/p') in
	*\"passthrough\"*) ;;
	*)
		host=$(hostname 2>/dev/null | cut -d. -f1)
		by_hand=""
		[ -n "$PORT" ] && by_hand=" Meanwhile, forward it yourself: ssh -N -L $PORT:localhost:$PORT ${host:-<server>}"
		die 5 "Nearhost on $(field laptop "$HELLO") is too old for passthrough; update it there.$by_hand"
		;;
	esac
	REQUESTER=$(requester)
	SESSION=$(session)
}

# pass_post POSTS the request, with the stored token. A token Nearhost no
# longer knows is dropped and the request asked again without it.
pass_post() {
	tok=""
	tf=""
	if [ "$REQUESTER" != agent ] && tf=$(token_file) && [ -f "$tf" ]; then
		IFS= read -r tok <"$tf" || :
		case $tok in
		*[!A-Za-z0-9_-]*) tok="" ;;
		esac
	fi
	attempt=0
	while :; do
		body="{\"port\":$PORT,\"requester\":$(quote "$REQUESTER"),\"session\":$(quote "$SESSION")"
		[ -n "$LABEL" ] && body="$body,\"label\":$(quote "$LABEL")"
		[ -n "$tok" ] && body="$body,\"token\":$(quote "$tok")"
		# The body goes on stdin, as it holds the token, which an argument
		# would show to anyone who lists this machine's processes.
		call "$SOCK" 60 -X POST -H 'Content-Type: application/json' --data-binary @- http://nearhost/v1/passthroughs <<EOF ||
$body}
EOF
			die 2 "Nearhost stopped answering."
		if [ "$STATUS" = 401 ] && [ -n "$tok" ] && [ $attempt -eq 0 ]; then
			rm -f "$tf"
			tok=""
			attempt=1
			continue
		fi
		break
	done
}

# refused exits 1 for a document Nearhost refused to show, leaving the agent
# with its path to give the user.
refused() {
	printf '%s\n' "Nothing was opened. Tell the user the document is at $DOCUMENT." >&2
	exit 1
}

# doc_find finds Nearhost and checks that it shows documents.
doc_find() {
	find_socket
	need documents
	REQUESTER=$(requester)
	SESSION=$(session)
}

# doc_post POSTS the request for DOC under ROOT, with the stored documents
# token. A token Nearhost no longer knows is dropped and the request asked
# again without it.
doc_post() {
	tok=""
	tf=""
	if [ "$REQUESTER" != agent ] && tf=$(doc_token_file) && [ -f "$tf" ]; then
		IFS= read -r tok <"$tf" || :
		case $tok in
		*[!A-Za-z0-9_-]*) tok="" ;;
		esac
	fi
	attempt=0
	while :; do
		body="{\"path\":$(quote "$DOC"),\"root\":$(quote "$ROOT"),\"requester\":$(quote "$REQUESTER"),\"session\":$(quote "$SESSION")"
		[ -n "$LABEL" ] && body="$body,\"label\":$(quote "$LABEL")"
		[ -n "$tok" ] && body="$body,\"token\":$(quote "$tok")"
		# The body goes on stdin, as it holds the token.
		call "$SOCK" 60 -X POST -H 'Content-Type: application/json' --data-binary @- http://nearhost/v1/documents <<EOF ||
$body}
EOF
			die 2 "Nearhost stopped answering."
		if [ "$STATUS" = 401 ] && [ -n "$tok" ] && [ $attempt -eq 0 ]; then
			rm -f "$tf"
			tok=""
			attempt=1
			continue
		fi
		break
	done
}

# --remote REQUESTER SESSION comes first when run_on runs the script on
# another host: sh -s reads the arguments and the script from stdin, over
# SSH. Nothing else may use it.
REMOTE=""
if [ "${1:-}" = --remote ]; then
	if [ -t 0 ] || [ -z "${SSH_CONNECTION:-}" ]; then
		die 1 "--remote is only for the script that --on runs over SSH."
	fi
	[ $# -ge 3 ] || die 1 "--remote needs the requester and the session"
	case $2 in
	claude-code | agent) ;;
	*) die 1 "--remote needs the requester claude-code or agent, not $2" ;;
	esac
	REMOTE=1
	REMOTE_REQUESTER=$2
	REMOTE_SESSION=$3
	shift 3
fi

cmd=${1:-}
[ $# -gt 0 ] && shift
case $cmd in
socket)
	find_socket
	printf '%s\n%s\n' "$SOCK" "$HELLO"
	;;
status)
	all=""
	if [ "${1:-}" = "--all" ]; then
		all=1
		shift
	fi
	find_socket
	if [ -n "$all" ]; then
		call "$SOCK" 10 http://nearhost/v1/projects || die 2 "Nearhost stopped answering."
	else
		dir=$(absolute "${1:-.}") || exit 1
		call "$SOCK" 10 -G --data-urlencode "path=$dir" --data-urlencode "real=$(resolved "$dir")" http://nearhost/v1/projects ||
			die 2 "Nearhost stopped answering."
	fi
	printf '%s\n' "$BODY"
	# 404: no project holds the path, so it is not in Nearhost yet.
	[ "$STATUS" = 200 ] || exit 1
	;;
propose)
	web=0
	name=""
	while [ $# -gt 0 ]; do
		case $1 in
		--web)
			# dash exits on a failed shift, so check the count first.
			[ $# -ge 2 ] || die 1 "--web needs a port"
			web=$2
			shift 2
			;;
		--name)
			[ $# -ge 2 ] || die 1 "--name needs a name"
			name=$2
			shift 2
			;;
		*) break ;;
		esac
	done
	case $web in
	'' | *[!0-9]*) die 1 "--web must be a port number, not $web" ;;
	esac
	dir=$(absolute "${1:-.}") || exit 1
	TARGET=$dir
	find_socket
	body="{\"path\":$(quote "$dir"),\"real\":$(quote "$(resolved "$dir")"),\"web\":$web"
	[ -n "$name" ] && body="$body,\"name\":$(quote "$name")"
	call "$SOCK" 10 -X POST -H 'Content-Type: application/json' --data-binary "$body}" http://nearhost/v1/proposals ||
		die 2 "Nearhost stopped answering."
	answer add
	;;
wait)
	id=${1:-}
	case $id in
	'' | *[!0-9a-f]*) die 1 "wait needs the id propose printed" ;;
	esac
	find_socket
	call "$SOCK" 60 "http://nearhost/v1/proposals/$id?wait=50" || die 2 "Nearhost stopped answering: it may have quit."
	printf '%s\n' "$BODY"
	[ "$STATUS" = 200 ] || exit 1
	case $(field state "$BODY") in
	declined) exit 6 ;;
	expired) exit 7 ;;
	superseded) exit 8 ;;
	esac
	;;
doctor)
	dir=$(absolute "${1:-.}") || exit 1
	TARGET=$dir
	find_socket
	need doctor
	call "$SOCK" 60 -G --data-urlencode "path=$dir" --data-urlencode "real=$(resolved "$dir")" http://nearhost/v1/doctor ||
		die 2 "Nearhost stopped answering."
	printf '%s\n' "$BODY"
	[ "$STATUS" = 200 ] || exit 1
	;;
reload)
	dir=$(absolute "${1:-.}") || exit 1
	TARGET=$dir
	find_socket
	need reload
	call "$SOCK" 60 -X POST -H 'Content-Type: application/json' --data-binary "{\"path\":$(quote "$dir"),\"real\":$(quote "$(resolved "$dir")")}" http://nearhost/v1/projects/reload ||
		die 2 "Nearhost stopped answering."
	# 200 is either applied or declined: the user already refused this change.
	if [ "$STATUS" = 200 ] && [ "$(field state "$BODY")" = declined ]; then
		printf '%s\n' "$BODY"
		exit 6
	fi
	answer
	;;
passthrough)
	PORT=""
	LABEL=""
	on=""
	while [ $# -gt 0 ]; do
		case $1 in
		--label)
			[ $# -ge 2 ] || die 1 "--label needs a name"
			LABEL=$2
			shift 2
			;;
		--on)
			[ $# -ge 2 ] || die 1 "--on needs the SSH host the dev server runs on"
			check_host "$2"
			on=$2
			shift 2
			;;
		*)
			[ -z "$PORT" ] || die 1 "passthrough takes one port"
			PORT=$1
			shift
			;;
		esac
	done
	case $PORT in
	'' | *[!0-9]* | 0*) die 1 "passthrough needs the server's port, such as: passthrough 3000" ;;
	esac
	[ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ] || die 1 "port must be between 1 and 65535, not $PORT"
	if [ -n "$on" ]; then
		if [ -n "$LABEL" ]; then
			run_on "$on" passthrough "$PORT" --label "$LABEL"
		fi
		run_on "$on" passthrough "$PORT"
	fi
	PASSPORT=$PORT
	pass_find
	pass_post
	held_back passthrough
	case $STATUS in
	200 | 201)
		save_token
		show_body
		[ "$(field state "$BODY")" = open ] && save_session
		exit 0
		;;
	202)
		printf '%s\n' "$BODY"
		approve_link
		save_session
		exit 0
		;;
	409)
		# Nothing on the laptop can ask the user, or the server dropped off.
		# An older Nearhost says it runs as nearhost up.
		case $BODY in
		*"has no page or window to ask you in"* | *"nearhost up"*) answer ;;
		*"isn't connected"*)
			host=$(hostname 2>/dev/null | cut -d. -f1)
			die 2 "Nearhost isn't connected to this server. On your laptop, select ${host:-<server>} in Nearhost and press c, then ask again. Or forward it yourself: ssh -N -L $PORT:localhost:$PORT ${host:-<server>}"
			;;
		esac
		printf '%s\n' "$BODY"
		exit 1
		;;
	*)
		printf '%s\n' "$BODY"
		exit 1
		;;
	esac
	;;
passthrough-wait)
	id=""
	on=""
	while [ $# -gt 0 ]; do
		case $1 in
		--on)
			[ $# -ge 2 ] || die 1 "--on needs the SSH host the dev server runs on"
			check_host "$2"
			on=$2
			shift 2
			;;
		*)
			[ -z "$id" ] || die 1 "passthrough-wait takes one id"
			id=$1
			shift
			;;
		esac
	done
	case $id in
	'' | *[!0-9a-f]*) die 1 "passthrough-wait needs the id passthrough printed" ;;
	esac
	[ -z "$on" ] || run_on "$on" passthrough-wait "$id"
	PORT=""
	pass_find
	call "$SOCK" 60 "http://nearhost/v1/passthroughs/$id?wait=50" || die 2 "Nearhost stopped answering: it may have quit."
	[ "$STATUS" = 200 ] || {
		printf '%s\n' "$BODY"
		exit 1
	}
	case $(field state "$BODY") in
	open)
		save_token
		save_session
		;;
	closed) save_token ;;
	esac
	show_body
	case $(field state "$BODY") in
	declined)
		held passthrough
		# The user's No has no reason. A reason means the user said yes and
		# opening the port failed, which is no answer to stop asking for.
		reason=$(text_field reason "$BODY")
		[ -n "$reason" ] && die 1 "Nearhost could not open the port: $reason"
		exit 6
		;;
	expired) exit 7 ;;
	esac
	;;
passthrough-close)
	# This runs when a session ends, wherever that was: it must stay quiet,
	# quick and successful.
	SOFT=1
	if [ "${1:-}" = --session ]; then
		sid=${2:-}
		plain_word "$sid" || exit 0
		if [ -n "$REMOTE" ]; then
			# Run by --on from the agent's machine, which keeps the marker:
			# the exit status says whether the close needs trying again.
			# No Nearhost here means there is nothing left to close.
			SOFT=""
			NONE_OK=1
			find_socket
			call "$SOCK" 1 -X DELETE "http://nearhost/v1/passthroughs?session=$sid" || exit 1
			[ "$STATUS" = 204 ] || exit 1
			exit 0
		fi
		close_session "$sid"
		exit 0
	fi
	id=""
	on=""
	while [ $# -gt 0 ]; do
		case $1 in
		--on)
			[ $# -ge 2 ] || die 1 "--on needs the SSH host the dev server runs on"
			check_host "$2"
			on=$2
			shift 2
			;;
		*)
			[ -z "$id" ] || die 1 "passthrough-close takes one id"
			id=$1
			shift
			;;
		esac
	done
	case $id in
	'' | *[!0-9a-f]*) die 1 "usage: passthrough-close --session <id> | <id> [--on host]" ;;
	esac
	[ -z "$on" ] || run_on "$on" passthrough-close "$id"
	find_socket
	call "$SOCK" 20 -X DELETE "http://nearhost/v1/passthroughs/$id" || exit 0
	[ "$STATUS" = 204 ] || printf '%s\n' "$BODY"
	exit 0
	;;
open)
	LABEL=""
	file=""
	while [ $# -gt 0 ]; do
		case $1 in
		--label)
			[ $# -ge 2 ] || die 1 "--label needs a name"
			LABEL=$2
			shift 2
			;;
		*)
			[ -z "$file" ] || die 1 "open takes one document"
			file=$1
			shift
			;;
		esac
	done
	[ -n "$file" ] || die 1 "open needs the document's path, such as: open docs/plan.md"
	[ -f "$file" ] || die 1 "$file is not a file here."
	# The folder is resolved, as git reports the root that way.
	fdir=$(cd "$(dirname "$file")" && pwd -P) || die 1 "$file is not a file here."
	DOC="$fdir/$(basename "$file")"
	DOCUMENT=$DOC
	ROOT=$(git -C "$fdir" rev-parse --show-toplevel 2>/dev/null) || ROOT=""
	[ -n "$ROOT" ] || ROOT=$fdir
	doc_find
	doc_post
	held_back document
	case $STATUS in
	200 | 201)
		save_token "$(doc_token_file)"
		show_body
		exit 0
		;;
	202)
		printf '%s\n' "$BODY"
		approve_link
		exit 0
		;;
	409)
		# Nothing on the laptop can ask the user, or the server dropped off.
		# An older Nearhost says it runs as nearhost up.
		msg=$(text_field error "$BODY")
		case $BODY in
		*"has no page or window to ask you in"* | *"nearhost up"*) die 3 "${msg:-Nearhost on the laptop has no page or window to ask you in; open nearhost there, then ask again.}" ;;
		*"isn't connected"*) die 2 "$msg" ;;
		esac
		printf '%s\n' "$BODY"
		refused
		;;
	*)
		printf '%s\n' "$BODY"
		refused
		;;
	esac
	;;
open-wait)
	id=${1:-}
	case $id in
	'' | *[!0-9a-f]*) die 1 "open-wait needs the id open printed" ;;
	esac
	doc_find
	call "$SOCK" 60 "http://nearhost/v1/documents/$id?wait=50" || die 2 "Nearhost stopped answering: it may have quit."
	[ "$STATUS" = 200 ] || {
		printf '%s\n' "$BODY"
		exit 1
	}
	[ "$(field state "$BODY")" = open ] && save_token "$(doc_token_file)"
	show_body
	case $(field state "$BODY") in
	declined)
		held document
		# The user's No has no reason. A reason means the user said yes and
		# showing the document failed.
		reason=$(text_field reason "$BODY")
		[ -n "$reason" ] && die 1 "Nearhost could not show the document: $reason"
		exit 6
		;;
	expired) exit 7 ;;
	esac
	;;
*)
	die 1 "usage: nearhost.sh socket | status [--all] [path] | propose [--web N] [--name X] [path] | wait <id> | doctor [path] | reload [path] | passthrough <port> [--label X] [--on host] | passthrough-wait <id> [--on host] | passthrough-close --session <id> | <id> [--on host] | open <path> [--label X] | open-wait <id>"
	;;
esac
