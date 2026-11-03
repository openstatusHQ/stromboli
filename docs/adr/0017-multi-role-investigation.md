# 0017. Multi-role investigation

- **Status:** Proposed
- **Date:** 2026-11-02

## Context

One agent alone tends to confirm its first guess and loses track in long incidents (context rot). ResolveAI landed on Investigator, Verifier and Communicator roles after three rewrites. Today opensre runs a single agent ([0010](0010-investigation-flow.md)).

## Decision

After the brief and the mitigation:

1. **Hypothesize.** The Investigator proposes up to 3 hypotheses, each with a plan for gathering evidence.
2. **Fan out.** One sub-investigation per hypothesis runs in parallel, each with its own small context and tool budget (for example 10 calls).
3. **Verify.** A separate Verifier gets *only the evidence*, not the investigator's reasoning, and tries to refute each hypothesis: `supported`, `refuted` or `inconclusive`, with confidence. It should run on a different model than the Investigator.
4. **Report.** A Communicator writes the update on a cheaper model.
5. **Steer.** Replies in the Slack thread ("check the db", "ignore deploy X") go back into the loop as instructions.
6. **Close.** When a human resolves the incident, opensre asks for the real root cause (one click) and stores it as eval data and a source of memory notes.

## Consequences

- To be accepted only if it beats the single agent on the evaluation set ([0019](0019-evaluation-harness.md)).
- More LLM calls per investigation, partly offset by cheaper models for some roles.
