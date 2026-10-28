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
#   nearhost.sh passthrough <port> [--label X]          open a server's port on the laptop
#   nearhost.sh passthrough-wait <id>                   wait up to 50s for the answer
#   nearhost.sh passthrough-close --session <id> | <id> close a session's passthroughs, or one
#   nearhost.sh open <path> [--label X]                 open a document in the user's browser
#   nearhost.sh open-wait <id>                          wait up to 50s for the answer
#
# It prints the API's JSON. Exit status: 0 done, 1 refused or failed,
# 2 no Nearhost reachable or no curl to reach it with, 3 Nearhost runs without its interface, 4 several
# laptops answer, 5 API mismatch or a call this Nearhost lacks, 6 declined
# by the user, 7 expired, 8 superseded: the change waited on no longer waits.
# passthrough-close always exits 0: it runs when a session ends.

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
		2) [ "${1#curl}" = "$1" ] && printf '%s\n' "Nearhost isn't running or connected, so nothing was opened. Tell the user the document is at $DOCUMENT." >&2 ;;
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
# nothing answers on SOCKET.
call() {
	sock=$1
	limit=$2
	shift 2
	out=$(curl -sS --unix-socket "$sock" --max-time "$limit" -w '\n%{http_code}' "$@" 2>/dev/null) || return 1
	STATUS=$(printf '%s\n' "$out" | tail -n 1)
	BODY=$(printf '%s\n' "$out" | sed '$d')
}

# no_nearhost says what to do when no Nearhost answers here.
no_nearhost() {
	if [ -n "${SSH_CONNECTION:-}" ]; then
		host=$(hostname 2>/dev/null | cut -d. -f1)
		if [ -n "${PASSPORT:-}" ]; then
			die 2 "Nearhost isn't connected to this server. On your laptop, select ${host:-<server>} in Nearhost and press c, then ask again. Or forward it yourself: ssh -N -L $PASSPORT:localhost:$PASSPORT ${host:-<server>}"
		fi
		# Nearhost forwards its socket only while it holds a connection to
		# the server, which it makes when one of the server's projects starts.
		die 2 "No Nearhost reaches this server. On your laptop, start nearhost there and connect this server (select it and press c). If this server is not in Nearhost yet, run this on the laptop instead: nearhost add --server ${host:-<server>} ${TARGET:-$PWD}"
	fi
	if command -v nearhost >/dev/null 2>&1; then
		die 2 "Nearhost is not running on this machine. Run: nearhost add <path>, then start nearhost to serve it."
	fi
	die 2 "Nearhost is not installed here. See https://nearhost.dev/docs/"
}

# find_socket sets SOCK and HELLO, the answer to GET /v1, or exits.
find_socket() {
	command -v curl >/dev/null 2>&1 || die 2 "curl is needed to reach Nearhost and is not installed here. Install curl, then run this again."
	if [ -n "${NEARHOST_SOCKET:-}" ]; then
		if ! call "$NEARHOST_SOCKET" 2 http://nearhost/v1 || [ "$STATUS" != 200 ]; then
			die 2 "Nothing answers on NEARHOST_SOCKET ($NEARHOST_SOCKET). Unset it to look in ~/.nearhost."
		fi
		SOCK=$NEARHOST_SOCKET
		HELLO=$BODY
	else
		found=0
		list=""
		for s in "$HOME"/.nearhost/*.sock; do
			[ -S "$s" ] || continue
			# Sockets that do not answer are left by runs that have ended.
			call "$s" 2 http://nearhost/v1 || continue
			[ "$STATUS" = 200 ] || continue
			found=$((found + 1))
			list="$list  NEARHOST_SOCKET=$s  ($(field laptop "$BODY"))
"
			SOCK=$s
			HELLO=$BODY
		done
		case $found in
		0) no_nearhost ;;
		1) ;;
		*)
			printf 'Nearhost answers here from %s laptops. Ask which one, then set NEARHOST_SOCKET:\n%s' "$found" "$list" >&2
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
# environment, which leaves backslashes as they are.
quote() {
	QUOTE=$1 awk 'BEGIN {
		s = ENVIRON["QUOTE"]
		gsub(/\\/, "\\\\", s)
		gsub(/"/, "\\\"", s)
		gsub(/\t/, "\\t", s)
		gsub(/\n/, "\\n", s)
		gsub(/\r/, "\\r", s)
		printf "\"%s\"", s
	}'
}

# answer prints BODY and exits for STATUS: 0 for success, otherwise with the
# API's error.
answer() {
	printf '%s\n' "$BODY"
	case $STATUS in
	2??) exit 0 ;;
	409) exit 3 ;;
	*) exit 1 ;;
	esac
}

# requester prints who is asking: claude-code inside Claude Code, otherwise
# agent. Codex documents no variable to tell it by, so it sends agent.
requester() {
	if [ "${CLAUDECODE:-}" = 1 ]; then
		printf 'claude-code'
	else
		printf 'agent'
	fi
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

# save_session notes that this session has an open passthrough, and on which
# socket, so that passthrough-close --session can find it without looking.
save_session() {
	plain_word "${SESSION:-}" || return 0
	private_dir "$(state_dir)" "$(state_dir)/sessions" || return 0
	private_file "$(state_dir)/sessions/$SESSION" "$SOCK"
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
	SESSION=${CLAUDE_CODE_SESSION_ID:-}
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

# doc_find finds Nearhost and checks that it shows documents.
doc_find() {
	find_socket
	need documents
	REQUESTER=$(requester)
	SESSION=${CLAUDE_CODE_SESSION_ID:-}
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
	answer
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
	while [ $# -gt 0 ]; do
		case $1 in
		--label)
			[ $# -ge 2 ] || die 1 "--label needs a name"
			LABEL=$2
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
	PASSPORT=$PORT
	pass_find
	pass_post
	case $STATUS in
	200 | 201)
		save_token
		show_body
		[ "$(field state "$BODY")" = open ] && save_session
		exit 0
		;;
	202)
		printf '%s\n' "$BODY"
		save_session
		exit 0
		;;
	409)
		# Nearhost runs as nearhost up, or the server dropped off.
		case $BODY in
		*"nearhost up"*) answer ;;
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
	id=${1:-}
	case $id in
	'' | *[!0-9a-f]*) die 1 "passthrough-wait needs the id passthrough printed" ;;
	esac
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
		marker="$(state_dir)/sessions/$sid"
		[ -f "$marker" ] || exit 0
		IFS= read -r SOCK <"$marker" || :
		# With its socket gone, that Nearhost is not running, and its
		# passthroughs ended with it, so there is nothing left to close.
		if [ ! -S "$SOCK" ]; then
			rm -f "$marker"
			exit 0
		fi
		command -v curl >/dev/null 2>&1 || exit 0
		# Otherwise the marker stays until Nearhost confirms the close, so a
		# later attempt can still find the socket.
		if call "$SOCK" 1 -X DELETE "http://nearhost/v1/passthroughs?session=$sid" && [ "$STATUS" = 204 ]; then
			rm -f "$marker"
		fi
		exit 0
	fi
	id=${1:-}
	case $id in
	'' | *[!0-9a-f]*) die 1 "usage: passthrough-close --session <id> | <id>" ;;
	esac
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
	case $STATUS in
	200 | 201)
		save_token "$(doc_token_file)"
		show_body
		exit 0
		;;
	202)
		printf '%s\n' "$BODY"
		exit 0
		;;
	409)
		# Nearhost runs as nearhost up, or the server dropped off.
		msg=$(text_field error "$BODY")
		case $BODY in
		*"nearhost up"*) die 3 "${msg:-Nearhost runs as nearhost up, which cannot show prompts.}" ;;
		*"isn't connected"*) die 2 "$msg" ;;
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
	die 1 "usage: nearhost.sh socket | status [--all] [path] | propose [--web N] [--name X] [path] | wait <id> | doctor [path] | reload [path] | passthrough <port> [--label X] | passthrough-wait <id> | passthrough-close --session <id> | <id> | open <path> [--label X] | open-wait <id>"
	;;
esac
