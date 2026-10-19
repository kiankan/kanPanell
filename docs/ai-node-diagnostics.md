# AI node diagnostics and repair workflow

## Current implementation scope

The AI Assistant panel adds a node diagnostics workbench that only calls existing panel APIs:
- `GET /panel/api/nodes` for the node list
- `POST /panel/api/nodes/probe/{id}` for connectivity/status probing

It displays observed findings, evidence, a proposed next-step plan, severity, and an observation timestamp. The acknowledgement button records local UI state only and deliberately does not mutate a node.

## Why automated repair is not enabled yet

The current repository exposes node probe and configuration/update APIs, but there is no dedicated server-side repair-plan API that validates a limited set of safe repair operations, requires a time-bound approval, and produces an immutable audit record. Directly wiring an LLM to generic node mutations would allow model output to cause unreviewed or unsafe changes.

Before enabling repairs, implement a server-side allowlist with:
1. A diagnostic snapshot ID and versioned plan.
2. Explicit, authenticated confirmation bound to the exact plan hash and node ID.
3. A short approval expiry and replay prevention.
4. Server-side revalidation of node state immediately before execution.
5. Per-operation allowlist and risk classification; no arbitrary shell commands.
6. Audit entries for actor, evidence references, plan, approval, execution result and rollback status.
7. Tests for stale approval, cross-node requests, replay, unauthorized users, and operation failures.

## LLM provider integration

No external model API is called by this page. Provider credentials should be stored server-side and never exposed in frontend settings or logs. The next increment should add an explicit provider interface, allowlisted model catalog, request timeouts, rate limits, data minimization, and tests with mocked providers. User/node data must not be sent to a provider unless the operator configures and explicitly enables that behavior.
