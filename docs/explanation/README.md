# Explanation

Explanation pages are for understanding. They describe how stromboli works as a whole, the trade-offs behind it, and the alternatives it turned down. Read them away from the keyboard; for steps to follow, see the [how-to guides](../how-to/README.md), and for exact values, the [reference](../reference/README.md).

| Page | Covers |
|---|---|
| [Architecture](architecture.md) | the pieces of stromboli, how a request flows through them, and where each lives in the code |
| [The life of an investigation](investigation-lifecycle.md) | from an alert to a report: matching, dedupe, the brief, the two phases, notifications and the audit trail |
| [Mitigate first, investigate second](mitigate-first.md) | why stromboli publishes a mitigation before it knows the root cause, and why from a closed set |
| [Safety model](safety-model.md) | how stromboli stays read-only, what the one exception is, and what protects your data and credentials |
| [Data access: on demand, MCP first](data-access.md) | why stromboli queries data where it lives instead of ingesting it, and when it uses MCP |
| [The service catalog](service-catalog.md) | why stromboli keeps its own catalog, and what it uses it for |

## Architecture decision records

The [ADRs](../adr/README.md) record each design decision when it was made: its context, the decision and its consequences. These explanation pages describe the result as it stands; the ADRs keep the history, including decisions still **Proposed**, such as [multi-role investigation](../adr/0017-multi-role-investigation.md), [memory notes](../adr/0018-memory-notes.md) and an [evaluation harness](../adr/0019-evaluation-harness.md).
