#!/bin/sh
# Scans the agent skill with Snyk Agent Scan, the scan skills.sh shows as
# "Snyk" on the skill's page, so a warning shows up here before it goes public.
# skill-scan.yml runs it for pull requests that change the skill; to run it
# yourself, from the repository's root:
#
#   SNYK_TOKEN=... sh .github/scripts/skill-scan.sh
#
# From the environment:
#   SNYK_TOKEN  a Snyk API token (app.snyk.io/account), not a `snyk auth` login
#
# Needs uvx and jq. Exits 1 on any finding, with its file and line, or when
# the scan fails or finds no skill. Snyk's free tier allows few scans a day:
# when it refuses one, the script says so and exits 0, since nothing was
# scanned.
set -eu

version=0.7.1
skills=agents/nearhost/skills

[ -n "${SNYK_TOKEN:-}" ] || {
	echo "SNYK_TOKEN is not set: Snyk Agent Scan needs an API token from https://app.snyk.io/account" >&2
	exit 1
}

# In a workflow, findings become annotations on the lines they cite.
gh=${GITHUB_ACTIONS:-false}

out=$(uvx --quiet "snyk-agent-scan@$version" scan --json "$skills")

errors=$(printf '%s' "$out" | jq -r '
	[.scan_path_responses[] | .error, .skill_risks[].error]
	| map(select(. != null)) | .[] | "\(.exception // "")\t\(.message)"')
if [ -n "$errors" ]; then
	if printf '%s\n' "$errors" | grep -q '^429'; then
		msg="Snyk Agent Scan did not scan the skill: its daily limit is reached. Run the skill-scan workflow again tomorrow."
		if [ "$gh" = true ]; then echo "::warning title=Skill not scanned::$msg"; else echo "$msg" >&2; fi
		exit 0
	fi
	printf '%s\n' "$errors" | cut -f2 | while IFS= read -r msg; do
		if [ "$gh" = true ]; then echo "::error title=Snyk Agent Scan failed::$msg"; else echo "Snyk Agent Scan failed: $msg" >&2; fi
	done
	exit 1
fi

count=$(printf '%s' "$out" | jq '[.scan_path_responses[].skill_risks[]] | length')
[ "$count" -gt 0 ] || {
	echo "Snyk Agent Scan found no skill in $skills" >&2
	exit 1
}

# One line per finding: skill, risk, score, first file and line cited, all
# places cited, and the evidence. Locations are relative to the skill's
# directory, which is named after the skill.
findings=$(printf '%s' "$out" | jq -r '
	.scan_path_responses[].skill_risks[] as $s
	| $s.risk_indexes | to_entries[] | select(.value != null)
	| (.value.locations // []) as $l
	| [$s.name, .key, (.value.score | tostring),
	   ($l[0].start.path // "SKILL.md"), ($l[0].start.line // 1 | tostring),
	   ([$l[] | "\(.start.path):\(.start.line // 1)"] | join(", ")),
	   (.value.evidence | gsub("\\s+"; " "))]
	| join("\t")')
if [ -z "$findings" ]; then
	echo "Snyk Agent Scan: no findings in $count skill(s)."
	exit 0
fi

printf '%s\n' "$findings" | while IFS="$(printf '\t')" read -r skill risk score file line places evidence; do
	if [ "$gh" = true ]; then
		echo "::error file=$skills/$skill/$file,line=$line,title=Snyk $risk ($score/1000)::$evidence Cited at: $places"
	else
		echo "$skill: $risk ($score/1000) at $places"
		echo "  $evidence"
	fi
done
exit 1
