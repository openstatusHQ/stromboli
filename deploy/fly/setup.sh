#!/usr/bin/env bash
# Deploy opensre to Fly.io from fly.toml, with a Fly Managed Postgres database.
#
#   FLY_MPG_CLUSTER=<cluster-id> ANTHROPIC_API_KEY=sk-... deploy/fly/setup.sh <app-name> [org]
#   OPENSRE_DB_URL=postgres://...  ANTHROPIC_API_KEY=sk-... deploy/fly/setup.sh <app-name> [org]
#
# The database is needed once: on later runs OPENSRE_DB_URL is already a secret.
# Safe to re-run: it never replaces a secret that is already set (changing
# OPENSRE_SECRET_KEY would make stored connector credentials unreadable).
set -euo pipefail

cd "$(dirname "$0")/../.."
command -v fly >/dev/null || { echo "fly CLI not found: https://fly.io/docs/flyctl/install/" >&2; exit 1; }
command -v openssl >/dev/null || { echo "openssl not found" >&2; exit 1; }
fly auth whoami >/dev/null 2>&1 || { echo "not logged in: run 'fly auth login'" >&2; exit 1; }

app=${1:?usage: deploy/fly/setup.sh <app-name> [org]}
org=${2:-}

if ! fly status --app "$app" >/dev/null 2>&1; then
	fly apps create "$app" ${org:+--org "$org"} --yes
fi

existing=$(fly secrets list --app "$app")
has_secret() { grep -qw "$1" <<<"$existing"; }
set_secret() { # set_secret KEY VALUE, only if KEY is unset; the value goes through stdin, not argv
	if ! has_secret "$1"; then
		printf '%s=%s\n' "$1" "$2" | fly secrets import --app "$app" --stage >/dev/null
		echo "set $1"
	fi
}

if ! has_secret OPENSRE_DB_URL; then
	if [[ -n ${FLY_MPG_CLUSTER:-} ]]; then
		fly mpg attach "$FLY_MPG_CLUSTER" --app "$app" --variable-name OPENSRE_DB_URL
	elif [[ -n ${OPENSRE_DB_URL:-} ]]; then
		set_secret OPENSRE_DB_URL "$OPENSRE_DB_URL"
	else
		cat >&2 <<EOF
No database: set FLY_MPG_CLUSTER or OPENSRE_DB_URL. To create a Managed Postgres cluster:

  fly mpg create --name $app-db --region ams --plan Basic ${org:+--org $org}
  fly mpg list ${org:+--org $org}     # the cluster ID
EOF
		exit 1
	fi
fi

# Fly secrets can't be read back: print new ones once so they can be stored.
new=()
for spec in "OPENSRE_SECRET_KEY:-base64 32" "OPENSRE_API_TOKEN:-hex 32"; do
	name=${spec%%:*}
	if ! has_secret "$name"; then
		# shellcheck disable=SC2086
		value=$(openssl rand ${spec#*:})
		set_secret "$name" "$value"
		new+=("$name=$value")
	fi
done
if ! has_secret ANTHROPIC_API_KEY; then
	key=${ANTHROPIC_API_KEY:-}
	if [[ -z $key && -t 0 ]]; then
		read -rsp "ANTHROPIC_API_KEY (empty to skip, briefs work without it): " key
		echo
	fi
	[[ -n $key ]] && set_secret ANTHROPIC_API_KEY "$key"
fi

# One machine: health checks and the dedupe window assume a single instance.
fly deploy --app "$app" --ha=false

echo
echo "URL: https://$app.fly.dev"
if ((${#new[@]})); then
	echo
	echo "Store these in your secret manager now, Fly can't show them again."
	echo "Never change OPENSRE_SECRET_KEY: it decrypts the stored connector credentials."
	printf '  %s\n' "${new[@]}"
fi
