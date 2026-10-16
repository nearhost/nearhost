#!/bin/sh
# nearhost.sh is how the nearhost skill talks to Nearhost: the agent API on
# ~/.nearhost/<id>.sock, which Nearhost serves on the laptop it runs on and
# forwards to each server it is connected to. It needs only sh and curl.
#
#   nearhost.sh socket                                  find Nearhost
#   nearhost.sh status [--all] [path]                   the project holding path
#   nearhost.sh propose [--web N] [--name X] [path]     ask to add the project
#   nearhost.sh wait <id>                               wait up to 50s for the answer
#
# It prints the API's JSON. Exit status: 0 done, 1 refused or failed,
# 2 no Nearhost reachable, 3 Nearhost runs without its interface, 4 several
# laptops answer, 5 API mismatch, 6 declined, 7 expired.

set -u

API=1

die() {
	code=$1
	shift
	printf '%s\n' "$*" >&2
	exit "$code"
}

# field NAME JSON prints the last top-level NAME in JSON. The API writes
# objects with their keys sorted and nests only the project, which sorts
# before the fields read here, so the last match is the top-level one.
field() {
	printf '%s' "$2" | sed -n "s/.*\"$1\":\"\{0,1\}\([^\",}]*\)\"\{0,1\}.*/\1/p"
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
		die 2 "No Nearhost reaches this server. Start nearhost on your laptop. If this server is not in Nearhost yet, run this on the laptop first: nearhost add --server ${host:-<server>} $PWD"
	fi
	if command -v nearhost >/dev/null 2>&1; then
		die 2 "Nearhost is not running on this machine. Run: nearhost add <path>, then start nearhost to serve it."
	fi
	die 2 "Nearhost is not installed here. See https://nearhost.dev/docs/"
}

# find_socket sets SOCK and HELLO, the answer to GET /v1, or exits.
find_socket() {
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

# absolute PATH prints PATH as an absolute directory.
absolute() {
	(cd "$1" 2>/dev/null && pwd -P) || die 1 "$1 is not a directory here."
}

# quote STRING prints STRING as a JSON string.
quote() {
	printf '"%s"' "$(printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g')"
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
		call "$SOCK" 10 -G --data-urlencode "path=$dir" http://nearhost/v1/projects || die 2 "Nearhost stopped answering."
	fi
	printf '%s\n' "$BODY"
	;;
propose)
	web=0
	name=""
	while [ $# -gt 0 ]; do
		case $1 in
		--web)
			web=${2:-}
			shift 2 || die 1 "--web needs a port"
			;;
		--name)
			name=${2:-}
			shift 2 || die 1 "--name needs a name"
			;;
		*) break ;;
		esac
	done
	case $web in
	'' | *[!0-9]*) die 1 "--web must be a port number, not $web" ;;
	esac
	dir=$(absolute "${1:-.}") || exit 1
	find_socket
	body="{\"path\":$(quote "$dir"),\"web\":$web"
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
	esac
	;;
*)
	die 1 "usage: nearhost.sh socket | status [--all] [path] | propose [--web N] [--name X] [path] | wait <id>"
	;;
esac
