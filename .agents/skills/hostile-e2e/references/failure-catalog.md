# Failure catalog

This is a discovery checklist, not a mandatory matrix. Select faults that can violate the requested contract within actual supported operation.

| Surface | Faults to schedule | Contract question |
| --- | --- | --- |
| Input and authority | Missing, malformed, oversized, duplicated, reordered, forged, or stale input; conflicting identity; cross-tenant reference | Which input is authoritative, and which principal owns the effect? |
| Ordering and concurrency | Competing writers, double submit, out-of-order response, cancellation race, stale callback, lease expiry | Which order is promised, and who owns work after a handoff? |
| Transport | Disconnect before acceptance, disconnect after commit, delayed response, malformed or truncated body, explicit rejection, rate limit | Can the caller distinguish rejection from uncertain success, and is retry safe? |
| Persistence | Rollback, uniqueness conflict, deadlock, lost update, stale read, commit followed by crash, partial cross-system commit | Which state is durable, and what can a replay change? |
| Lifecycle | Worker crash, process shutdown with accepted work, restart, reconnect, deployment version mismatch | Does accepted work reach its defined outcome after recovery? |
| Time | Deadline expiry, clock skew, token expiration, timezone boundary, delayed scheduled work | Which clock defines authority and correctness? |
| Capacity | Queue pressure, exhausted pool, slow consumer, large supported input, resource leak | Can new work proceed after overload or failure? |
| External effect | Committed but unacknowledged payment, email, upload, or message; partial batch success; duplicate callback; failed compensation | What did the external peer actually observe? |
| Client experience | Navigate during work, offline/reconnect, stale optimistic state, refresh after ambiguous result | Does displayed state agree with committed state and current intent? |

Preserve a successful control journey so the failure result is interpretable. For every selected scenario, name the defect it would catch and the independent observable that proves it.
