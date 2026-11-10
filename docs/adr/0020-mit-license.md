# 0020. MIT license

- **Status:** Accepted
- **Date:** 2026-11-09

## Context

stromboli is meant to be self-hosted by the teams that run it ([0002](0002-self-hosted-one-instance-per-organisation.md)). Those teams connect it to their production logs, metrics and deploys, so they need to read, audit, patch and extend it, often inside companies whose legal teams treat copyleft licenses (AGPL in particular) as a blocker. The ADR index listed the choice as open: AGPL like Metabase, or a permissive license.

## Decision

stromboli is released under the [MIT license](../../LICENSE). Contributions are accepted under the same license (inbound = outbound), with no CLA.

## Consequences

- Anyone can self-host, fork, embed or modify stromboli, including in proprietary setups, with no obligation beyond keeping the copyright notice.
- Adoption is not slowed by license reviews.
- Others can offer stromboli as a hosted service without contributing back; we accept that trade-off.
- Changing to a more restrictive license later would only apply to new code and would need a new ADR.
