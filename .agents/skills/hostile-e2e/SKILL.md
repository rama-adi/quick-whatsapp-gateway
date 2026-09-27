---
name: hostile-e2e
description: Design, implement, or audit deterministic end-to-end tests for systems where concurrency, retries, partial failure, stale state, or external effects can break user-visible behavior.
---

# Hostile end-to-end testing

Prove what an actual user or external peer experiences when the environment behaves badly. This skill applies to web applications, APIs, background jobs, agents, databases, queues, integrations, and deployment workflows. It does not prescribe a framework or a fixed number of scenarios.

## Define the contract and boundary

Name the entry point, real components crossed by the test, dependencies replaced by controlled actors, authoritative state, and externally visible effects. State the promised safety, recovery, authority, atomicity, ordering, and cleanup properties that apply. Do not infer exactly-once delivery or total ordering when the product promises something else. Derive retry bounds, deadlines, and capacities from a protocol, product requirement, or measurement.

Map the journey from input through authorization, validation, persistence, external effects, acknowledgement, and a subsequent read. Mark each commit point where a crash or lost response can leave components disagreeing. Keep product decisions and persistence real. A fake database cannot prove real database isolation, constraints, locks, or crash durability; use the actual engine when those are the claim.

## Design controlled failure scenarios

Before implementation, write each scenario as **initial state → trigger → fault and exact schedule → recovery action → observable invariant**. Use the [failure catalog](references/failure-catalog.md) to discover relevant faults, then select only those that can violate this task's contract. Combine faults when their interaction matters: concurrent requests plus delayed responses, remote commit plus lost acknowledgement, restart plus replay, or stale callback plus a new owner.

Replace external providers at their protocol boundary with stateful actors that implement the provider's real wire contract, including idempotency and error formats. Control acceptance, commit, acknowledgement, and failure separately. Use barriers and deferred promises to force causal order; use a controllable clock only where product code consumes it. Random stress may supplement deterministic scenarios when the seed and event trace are preserved for replay.

## Prove outcomes independently

Drive the public UI, API, command, queue, or scheduler path. Observe effects independently from implementation counters and mock calls: rendered state, recipient transcript, rows read through a public query, durable job state, emitted webhook ledger, downloaded content, or provider-side committed effects. Distinguish transport attempts from commits. Verify the result after recovery and that subsequent legitimate work proceeds. For a failure case, assert both the effect that occurred and the effect that must not occur. Explain which named defect would make each assertion fail.

Do not mutate private state to manufacture success, make a fake compute expected results with the production algorithm, or pin incidental source text, internal call sequences, and snapshots. [Examples](references/examples.md) compare useful and misleading tests across domains.

## Evidence and test relevance

Produce a reproducible artifact from the run containing source identity, execution environment, exact command, scenario, initial state, injected events, transport attempts, committed effects, assertions, and result. Exclude secrets. Record failures and skips explicitly; neither proves the contract.

Prefer an end-to-end journey over shallow tests of the same behavior. Retain an isolated test when it catches a named failure the journey cannot reach, such as parser interoperability or a mathematical edge case with independent reference data. Delete tautological, redundant, and change-detector tests when stronger coverage replaces them. Repair a discovered product defect and rerun the scenario that exposed it. Stop when the requested contract is proved and no remaining relevant failure inventory item breaks it.
