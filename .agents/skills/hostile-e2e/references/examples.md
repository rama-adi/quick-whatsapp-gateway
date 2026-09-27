# Strong and weak test examples

## Checkout and payment

**Strong:** The fake payment provider commits a charge, then drops its response. The client retries the same checkout and refreshes its status. Inspect the provider's committed-charge ledger and the public order status. One charge and one order exist, and the result follows the product's idempotency contract. Replay the same key with changed order data and verify rejection.

**Weak:** Mock `charge()` to return an ID, call the handler once, and assert that it returned the ID. This bypasses retry, remote commit, and reconciliation.

## Browser search

**Strong:** Start search A, then B. Hold A's response until after B renders. Release A and inspect the visible results and selected query. They still show B. Navigate away during another request and verify that it cannot update the new page.

**Weak:** Call a private state setter twice and assert that its final argument was B. This bypasses rendering and stale-response ownership.

## Queue and worker restart

**Strong:** Enqueue a job, pause after its external effect commits but before the worker acknowledges it, stop the worker, and restart it. Inspect the durable queue and downstream effect ledger. The job reaches its declared terminal state and the effect obeys the declared delivery guarantee.

**Weak:** Invoke the handler twice against an in-memory repository and assert that a callback ran once. This does not test persistence or lease recovery.

## Database competing writers

**Strong:** Two clients reserve the last item through the public API. Block them at a controlled transaction point and release them in conflict. Read the final state through the API. There is one reservation, stock is nonnegative, and the losing request receives the defined response.

**Weak:** Test only a private `stock > 0` predicate or mock `save()` and assert that it was called. Neither detects a lost update.

## Authorization change

**Strong:** Load an editable page, revoke the account's permission, then submit through the existing browser session. The server rejects the write, persistent state is unchanged, and the UI reflects the rejection.

**Weak:** Hide the edit button and infer that writes are forbidden. The server may still accept a stale client request.

## Interrupted upload

**Strong:** Interrupt a multipart upload, resume it through the public API, finish it, and download the object through the normal path. Verify exact content and the system's cleanup rule for abandoned parts.

**Weak:** Assert that an internal upload helper was called with the expected chunk indexes. That cannot prove stored content or cleanup.

## Agent and message delivery

**Strong:** A scripted model runs the shipped command through the sandbox. A stateful fake delivery service commits the first bubble and withholds acknowledgement while another command waits. Inspect the recipient transcript, target, result status, and next-turn behavior. Feed invalid arguments and forged targets through the same command path.

**Weak:** Set `sentCount = 1` in a fake model callback, echo command input from a mock bridge, or assert only that `sendMessage` was called. None proves what the recipient saw or which authority the real command used.

## Evidence

Generate the artifact during the run. Include source identity, exact reproduction command, scenario, fault schedule, attempted requests, committed effects, and observed result. A handwritten green summary or mock-call count is not evidence of the external outcome.
