# How to manage the service catalog in git

The catalog lives in stromboli's database, and YAML is its import and export format. Keep the YAML and the runbooks in a repository, review changes in pull requests, and sync them from CI.

## Bootstrap the files from what is there

If the catalog already has entries, export them with their runbooks:

```sh
stromboli catalog export --runbooks runbooks/ > services.yaml
```

Otherwise, start from what a connector sees:

```sh
stromboli catalog discover loki > services.yaml
```

Lay the repository out like this; runbooks are referenced by their path under `runbooks/`:

```text
services.yaml
runbooks/
  api-5xx.md
  db-proxy-pool.md
```

```yaml
- name: api
  runbooks: [runbooks/api-5xx.md]
```

## Validate in pull requests

`--dry-run` validates the file against the server (schema, unknown dependencies, cycles, missing runbooks, unknown connectors) and changes nothing:

```sh
stromboli catalog import services.yaml --runbooks runbooks/ --dry-run
```

Errors exit with status 1; warnings (no owners, no log source…) are printed but do not fail.

## Sync on merge

`--prune` makes the file the source of truth: services and runbooks that are not in it are deleted. In CI there is no terminal to confirm, so pass `--yes`:

```sh
stromboli catalog import services.yaml --runbooks runbooks/ --prune --yes
```

A GitHub Actions job could look like this:

```yaml
name: catalog
on:
  pull_request: { paths: [services.yaml, "runbooks/**"] }
  push: { branches: [main], paths: [services.yaml, "runbooks/**"] }
jobs:
  sync:
    runs-on: ubuntu-latest
    env:
      STROMBOLI_URL: ${{ vars.STROMBOLI_URL }}
      STROMBOLI_API_TOKEN: ${{ secrets.STROMBOLI_API_TOKEN }}
      STROMBOLI_VERSION: 0.1.0   # the release your server runs
    steps:
      - uses: actions/checkout@v4
      - name: Install stromboli
        run: |
          name=stromboli_${STROMBOLI_VERSION}_linux_amd64
          curl -sSL "https://github.com/openstatushq/stromboli/releases/download/v${STROMBOLI_VERSION}/${name}.tar.gz" | tar xz
          mv "$name/stromboli" .
      - name: Validate
        if: github.event_name == 'pull_request'
        run: ./stromboli catalog import services.yaml --runbooks runbooks/ --dry-run
      - name: Sync
        if: github.event_name == 'push'
        run: ./stromboli catalog import services.yaml --runbooks runbooks/ --prune --yes
```

Release archives are named `stromboli_<version>_<os>_<arch>` (`linux`, `darwin` or `windows`; `amd64` or `arm64`) and come with a `checksums.txt`.

## Check what changed

```sh
stromboli catalog list                         # name, tier, owners, dependencies, sources
stromboli catalog validate --check-connectors  # also runs a test query per selector
```

## Map alerts to services

Alerts are matched to services by name, or by `monitors` for openstatus. Add the monitor IDs to the entry:

```yaml
- name: api
  monitors: ["openstatus:1234"]
```

See [Send alerts to stromboli](send-alerts.md).

## See also

- [Catalog reference](../reference/catalog.md): every field and validation rule
- [Why stromboli owns the catalog](../explanation/service-catalog.md)
