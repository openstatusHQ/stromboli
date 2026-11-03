#!/usr/bin/env bash
# Deploy opensre and Postgres to Railway from .railway/railway.go.
#
#   ANTHROPIC_API_KEY=sk-... deploy/railway/setup.sh [project-name]
#
# Safe to re-run: it applies the config again and never replaces a secret
# that is already set (changing OPENSRE_SECRET_KEY would make stored
# connector credentials unreadable).
set -euo pipefail

cd "$(dirname "$0")/../.."
command -v railway >/dev/null || { echo "railway CLI not found: https://docs.railway.com/cli" >&2; exit 1; }
command -v openssl >/dev/null || { echo "openssl not found" >&2; exit 1; }
railway whoami >/dev/null || { echo "not logged in: run 'railway login'" >&2; exit 1; }

if ! railway status >/dev/null 2>&1; then
	railway init --name "${1:-opensre}"
fi

railway config apply --yes

existing=$(railway variable list --service opensre --kv)
changed=0
set_secret() { # set_secret KEY VALUE, only if KEY is unset
	if ! grep -q "^$1=." <<<"$existing"; then
		printf '%s' "$2" | railway variable set "$1" --stdin --service opensre --skip-deploys >/dev/null
		echo "set $1"
		changed=1
	fi
}
set_secret OPENSRE_SECRET_KEY "$(openssl rand -base64 32)"
set_secret OPENSRE_API_TOKEN "$(openssl rand -hex 32)"
if ! grep -q "^ANTHROPIC_API_KEY=." <<<"$existing"; then
	key=${ANTHROPIC_API_KEY:-}
	if [[ -z $key && -t 0 ]]; then
		read -rsp "ANTHROPIC_API_KEY (empty to skip, briefs work without it): " key
		echo
	fi
	[[ -n $key ]] && set_secret ANTHROPIC_API_KEY "$key"
fi
if ((changed)); then
	railway redeploy --service opensre --yes >/dev/null
fi

# Railway-generated domains are not part of the IaC graph.
if railway domain list --service opensre | grep -q "railway.app"; then
	railway domain list --service opensre
else
	railway domain --service opensre --port 8080
fi

echo
echo "API token: railway variable list --service opensre --kv | grep OPENSRE_API_TOKEN"
