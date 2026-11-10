# How to connect deploys and commits from GitHub

Most incidents follow a change. With a GitHub connector, the brief lists recent deploys of a service and of its dependencies, and the agent can read a commit's message and diff with `get_commit`.

## 1. Create a token

Create a [fine-grained personal access token](https://github.com/settings/personal-access-tokens) (or a GitHub App token) with **read-only** access to the repositories of your services: *Contents*, *Actions*, *Deployments* and *Metadata*.

Public repositories work without a token, through the REST API, with GitHub's lower rate limit.

## 2. Add the connector

```sh
stromboli connector add github github token   # prompts for the token
```

With a token, stromboli goes through GitHub's remote MCP server in read-only mode. For GitHub Enterprise Server, add `base_url=https://github.example.com/api/v3`; that uses the REST API unless you also set `mcp_url`.

## 3. Tell each service what a deploy is

Set `deploys` on the service. `repo` is required; then pick one of three ways to find deploys:

```yaml
- name: api
  deploys:
    source: github
    repo: acme/api
    workflow: deploy.yml        # deploys are the completed runs of this workflow
```

```yaml
  deploys:
    source: github
    repo: acme/api
    environment: production     # deploys are GitHub deployments to this environment
```

```yaml
  deploys:
    source: github
    repo: acme/api
    branch: main                # neither: recent commits on this branch (default: the repo's default branch)
```

Import the file with `stromboli catalog import`.

## 4. Check it

```sh
stromboli brief api --since 4h
```

The **Recent changes** section lists deploys (or commits) of `api` and of the services it depends on.

## See also

- [Catalog reference: `deploys`](../reference/catalog.md#deploys)
- [Connectors reference: `github`](../reference/connectors.md#github)
