# Tutorial 2: connect a real service

In [tutorial 1](01-first-investigation.md) you ran stromboli against a demo. Now you will point it at one of **your** services: run stromboli with Docker Compose, connect the log store your service already writes to, describe the service in the catalog, and send stromboli an alert the way your alerting tool will.

By the end, a `curl` that looks like an alert will start an investigation of your service on its own.

You need:

- Docker with Compose, on a machine that can reach your log store
- an LLM API key (this tutorial uses `ANTHROPIC_API_KEY`)
- one service whose logs are in **Loki**, **Axiom**, **CloudWatch** or **Google Cloud Logging**, and read credentials for that store
- the `stromboli` CLI: download it from the [releases page](https://github.com/openstatushq/stromboli/releases), or use `docker compose exec stromboli stromboli ...` instead of `stromboli ...` throughout

The examples use Loki and a service called `api`. Replace them with yours as you go.

## 1. Configure stromboli

```sh
git clone https://github.com/openstatushq/stromboli && cd stromboli
cp .env.example .env
```

Generate the two secrets stromboli needs:

```sh
openssl rand -base64 32   # → STROMBOLI_SECRET_KEY
openssl rand -hex 32      # → STROMBOLI_API_TOKEN
```

Open `.env` and fill in:

```sh
STROMBOLI_SECRET_KEY=<the first value>
STROMBOLI_API_TOKEN=<the second value>
ANTHROPIC_API_KEY=sk-ant-...
```

The secret key encrypts the credentials you are about to give stromboli. Keep a copy: if it changes, stored credentials can no longer be decrypted.

## 2. Start it

```sh
docker compose up -d
```

This starts stromboli and Postgres on `http://localhost:8080`. Check it is healthy:

```sh
curl localhost:8080/healthz
```

```text
ok
```

Point your CLI at it, with the token from `.env`:

```sh
export STROMBOLI_URL=http://localhost:8080
export STROMBOLI_API_TOKEN=<the second value>
```

## 3. Connect your log store

Add a connector for your log store. For Loki:

```sh
stromboli connector add loki loki url=https://loki.example.com
```

If your store needs a password or token, name the key without a value and stromboli prompts for it, so the secret stays out of your shell history:

```sh
stromboli connector add loki loki url=https://loki.example.com username=12345 password
```

```text
password:
Connector "loki" saved.
Connector "loki" is reachable.
```

For other stores, the first command is one of these (all keys are in [Connectors](../reference/connectors.md)):

```sh
stromboli connector add axiom axiom token
stromboli connector add cw cloudwatch region=eu-west-1
stromboli connector add gcp-logs gcp_logging project=<project-id> credentials_json=@key.json
```

## 4. Let stromboli propose a catalog

Rather than writing the catalog by hand, ask the connector which services it sees:

```sh
stromboli catalog discover loki > services.yaml
```

```text
Found 7 services. Add owners and depends_on, then run `stromboli catalog import <file>`.
```

Open `services.yaml`. There is one entry per service, each with its `logs.source` and `logs.selector` filled in. Delete every entry except your service, then add who owns it and what it depends on:

```yaml
- name: api
  description: Public REST API
  owners: ["@team-backend"]
  tier: 1
  logs:
    source: loki
    selector: '{service_name="api"}'
```

Leave `depends_on` out for now: every dependency must also be in the catalog.

## 5. Import it

Check the file first. `--dry-run` validates without changing anything:

```sh
stromboli catalog import services.yaml --dry-run
```

```text
Dry run: the catalog is valid, nothing was changed.
```

Then import it and check that the log selector really returns data:

```sh
stromboli catalog import services.yaml
stromboli catalog validate --check-connectors
```

```text
The catalog is valid.
```

## 6. Look at the brief

```sh
stromboli brief api --since 1h
```

This is what stromboli knows about your service before any LLM is involved. Check the **Logs** section: if it shows error patterns you recognise, stromboli is looking at the right logs. If it says there are no logs, go back to step 4 and fix the selector.

## 7. Send it an alert

Alerting tools reach stromboli through webhooks under `/webhooks/`. Pretend to be one with the generic webhook, which accepts a service name and a title:

```sh
curl -X POST localhost:8080/webhooks/generic \
  -H "Authorization: Bearer $STROMBOLI_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"service":"api","title":"Error rate above 5%","fingerprint":"api-error-rate"}'
```

```json
{"alerts":[{"title":"Error rate above 5%","status":"started","investigation_id":"7c9e6679-..."}]}
```

An investigation started on its own. Follow it:

```sh
stromboli show <investigation_id> --follow
```

## 8. Send the same alert again

Alerting tools repeat themselves. Run the same `curl` again while the investigation runs:

```json
{"alerts":[{"title":"Error rate above 5%","status":"deduplicated","investigation_id":"7c9e6679-..."}]}
```

Same `fingerprint`, same investigation: stromboli merged the repeat instead of starting a second one. Now tell it the alert resolved:

```sh
curl -X POST localhost:8080/webhooks/generic \
  -H "Authorization: Bearer $STROMBOLI_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"service":"api","title":"Error rate above 5%","fingerprint":"api-error-rate","resolved":true}'
```

```json
{"alerts":[{"title":"Error rate above 5%","status":"resolved"}]}
```

The next alert with that fingerprint will start a new investigation.

## What you learned

- `catalog discover` turns what a connector sees into catalog entries you review and import.
- `catalog validate --check-connectors` proves a selector returns data.
- Alerts arrive on `/webhooks/*`, are matched to a catalog service, and start investigations.
- A fingerprint merges repeats of an alert into one investigation, until the alert resolves.

## Next

You now have the pieces. Make it production-ready with the how-to guides:

- [Self-host in production](../how-to/self-host.md): TLS, tokens, backups
- [Send alerts to stromboli](../how-to/send-alerts.md) from openstatus, Alertmanager or Grafana
- [Set up Slack](../how-to/set-up-slack.md) to get mitigations and reports in a channel
- [Connect metrics](../how-to/connect-metrics.md) and [deploys](../how-to/connect-github.md) so the brief shows them too
