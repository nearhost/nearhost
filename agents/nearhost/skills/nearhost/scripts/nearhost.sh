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
#
# It prints the API's JSON. Exit status: 0 done, 1 refused or failed,
# 2 no Nearhost reachable or no curl to reach it with, 3 Nearhost runs without its interface, 4 several
# laptops answer, 5 API mismatch or a call this Nearhost lacks, 6 declined,
# 7 expired, 8 superseded: the change waited on no longer waits.

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
*)
	die 1 "usage: nearhost.sh socket | status [--all] [path] | propose [--web N] [--name X] [path] | wait <id> | doctor [path] | reload [path]"
	;;
esac
