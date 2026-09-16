# Optional External Session State for the MCP Gateway

## Table of Contents

- [Background and Motivation](#background-and-motivation)
- [Current State: Header-Encoded Sessions](#current-state-header-encoded-sessions)
- [Goals and Non-Goals](#goals-and-non-goals)
- [Where This Lives](#where-this-lives)
- [Session Creation](#session-creation)
- [Record Contents](#record-contents)
- [Principal Binding](#principal-binding)
- [Reconnection and Cursor Acknowledgement](#reconnection-and-cursor-acknowledgement)
- [Session Store Contract](#session-store-contract)
- [Lifecycle, Deletion, and TTL](#lifecycle-deletion-and-ttl)
- [Admission and Dispatch](#admission-and-dispatch)
- [Record Format and Rolling Compatibility](#record-format-and-rolling-compatibility)
- [Datastore Security Boundary](#datastore-security-boundary)
- [Error Mapping](#error-mapping)
- [Coexistence with Header-Encoded Sessions](#coexistence-with-header-encoded-sessions)
- [Multi-Replica Operation](#multi-replica-operation)
- [Observability](#observability)
- [Required Test Coverage](#required-test-coverage)
- [Open Questions](#open-questions)

## Background and Motivation

[Proposal 006](../006-mcp-gateway/proposal.md) encodes all upstream MCP session state into a single encrypted session ID header. Its Future Work section notes that this "is not ideal" and suggests Redis or another external cache as an alternative. This proposal works that suggestion through in detail.

The header-encoded scheme has three properties that some deployments cannot accept:

1. **The client-visible session ID is not opaque.** It is an encrypted blob, so a client cannot read it — but its size and structure still reflect how many upstreams were selected.
2. **It grows with the number of matching backends.** A session that fans out to many upstreams produces a large header on every request for the life of the session.
3. **There is no shared lifecycle.** Nothing outside the token knows the session exists, so there is no way to enumerate, revoke, or expire sessions centrally.

None of these matter for the common case. Proposal 006's own reasoning holds: the number of matching backends per MCPRoute is expected to stay small. This proposal therefore adds an **opt-in** mode rather than replacing the default.

## Current State: Header-Encoded Sessions

Header-encoded session state keeps the deployment model simple. Any MCP Proxy replica can decrypt the session ID and reconstruct the upstream state with no shared datastore, no store outage to survive, and no cross-replica coordination. That is a genuine strength, not merely a limitation, and it is why it stays the default.

## Goals and Non-Goals

### Goals

- Provide an opt-in session mode where the client-visible session ID is opaque and fixed-size, and upstream state lives in an external store.
- Give the mode a fail-closed security posture: no request reaches an upstream unless the record validates.
- Define a vendor-neutral session-store contract, so the first implementation is not coupled to a specific datastore client.
- Keep header-encoded sessions working unchanged, including during a rolling upgrade and during a store outage.

### Non-Goals

- **Replacing header-encoded sessions.** The default does not change.
- **Adding a datastore dependency in this proposal.** No client library is selected and no dependency is added. A Redis-compatible backend such as Valkey is the first candidate to evaluate _after_ maintainers approve the contract.
- **Converting existing sessions.** Enabling the mode affects newly initialized sessions only.
- **Making the controller or the external rate-limit service a session datastore.** Neither should hold MCP session state.
- **Semantic caching or response caching.** Unrelated concerns that happen to also involve a datastore.

## Where This Lives

The MCP Proxy owns this extension point, in `internal/mcpproxy`. The controller continues to translate user configuration and Envoy continues to own networking, routing, and rate limiting.

This placement puts session validation, upstream session fan-out, and notification resumption beside the code that already implements those MCP semantics, rather than splitting session handling across two components.

## Session Creation

When external mode is enabled, the proxy reserves the session ID **before** it fans out `initialize` requests. This ordering is what makes the fail-closed guarantee possible; the reverse order would leave upstream sessions with no record to clean them up.

1. **Reserve.** Generate an opaque, cryptographically random session ID with at least 128 bits of entropy. It conveys no route, backend, tenant, or authentication information. Write a `pending` record with a short reservation TTL, under a namespace isolated by configured gateway scope and tenant or namespace. While pending, no request may load or use it.
2. **Fan out.** Send `initialize` to each matching upstream.
3. **Activate.** After at least one upstream initialization succeeds, atomically write only the **successfully initialized** backend entries and transition the record to `active`. Return the ID to the client with the normal aggregated initialize response, built only from that successful subset.
4. **Or roll back.** If no upstream initialization succeeds, or activation cannot be committed, do not return the ID. Perform best-effort deletion of each upstream session that did succeed, then remove the pending record.

The active record routes only to that immutable initialized subset. The proxy never retries or lazily adds a failed backend during the session. Failed backend names, identifiers, and error details are not exposed to the client.

## Record Contents

An active record holds only what is needed to route an MCP session:

- the MCP route identity and a canonical authenticated principal binding;
- for each selected backend: its name, upstream session ID, and negotiated capabilities;
- a schema version, a record revision, lifecycle state, creation and expiry metadata, and whatever is needed to validate the record.

Deliberately **not** stored: credentials, bearer tokens, cookies, forwarded request headers, and upstream event cursors observed by a proxy. Forwarded headers are derived from each incoming request using the route configuration, exactly as they are for header-encoded sessions.

## Principal Binding

The principal binding is recomputed from the authenticated request on every use and must match the record before the proxy uses any upstream session ID. A canonical principal includes the token issuer, tenant or namespace/policy context, and subject, each in its authentication provider's canonical form.

External mode rejects an initialization or subsequent session request that lacks a complete principal. It intentionally does not create anonymous bearer sessions: an opaque long-lived ID with no identity bound to it is a bearer token for someone else's upstream sessions.

**The authenticated frontend produces the principal, not the MCP Proxy.** After authenticating the request, the frontend strips any client-supplied identity-assertion fields and supplies one immutable `AuthenticatedPrincipal` assertion to the proxy over the trusted local Envoy-to-proxy hop. The handoff must be non-forgeable to clients — for example, trusted listener metadata protected by the local workload boundary — and bound to the authenticated request. It is not an ordinary HTTP header accepted from a caller.

The authentication integration canonicalizes issuer, tenant or policy context, and subject before creating the assertion. The proxy consumes that assertion for initialize, GET, POST, and DELETE, and must not derive durable identity from an unverified JWT claim. A missing assertion, an empty required component, conflicting values, or an assertion from an untrusted hop fails closed before record creation, loading, or upstream dispatch.

## Reconnection and Cursor Acknowledgement

The proxy must not treat an event it merely observed from an upstream — or even wrote to an HTTP response — as a durable reconnect checkpoint. Neither proves the client received it.

Instead, every downstream SSE event carries a cryptographically protected, versioned **resume token** containing the session ID and that event's complete per-backend upstream cursor snapshot. The token is immutable once emitted.

In external mode, only a subsequent GET may treat `Last-Event-ID` as an acknowledgement. POST and DELETE ignore the header and must never parse, decrypt, persist, or forward it. After validating a GET token against the active session and principal, the proxy decrypts it and supplies its cursor snapshot to the corresponding upstream streams. A missing token begins a fresh stream.

Concurrent streams therefore each use their own acknowledged token and cannot overwrite one another's progress. The store has no mutable "latest cursor" field, and never compares arbitrary upstream event-ID strings, which are backend-defined and need not be ordered.

### Bounding resume-token input

Resume-token input is bounded before any decryption or parsing. External-mode configuration defines positive limits for encoded token bytes, decoded plaintext bytes, number of backend cursor entries, each backend identifier, and each cursor. Each configured value is capped by a feature-wide hard maximum.

The proxy rejects a token exceeding an encoded limit **before** cryptographic work, then authenticates and decrypts it into a size-limited buffer before decoding its version, session ID, principal binding, backend count, and individual lengths. Duplicate backends, unknown backends, empty required fields, malformed encodings, and decoded components over their limits are all rejected before upstream dispatch. The proxy also refuses to _emit_ a token that would exceed those limits, ending the affected stream rather than writing that event.

### What this guarantees, and what it does not

The event path is fail-safe without claiming an impossible delivery acknowledgement:

- The proxy validates the resume token before dispatching upstream GET requests.
- If it cannot create or validate a token before an event is written, it terminates that stream without writing the event. The client reconnects with its prior token.
- Once an event **is** written, a connection failure is indistinguishable from a client that received it. The proxy persists nothing and relies on the client to reconnect with the last token it actually received.

Reusing an older valid token may replay events. Clients must tolerate replay per MCP's normal SSE reconnection model — but the gateway must never _skip_ an event solely because another replica observed it.

## Session Store Contract

The first implementation should depend on a vendor-neutral session-store contract rather than a datastore client. The contract needs:

| Operation                    | Purpose                                                 |
| ---------------------------- | ------------------------------------------------------- |
| create-pending               | Atomically reserve a session ID before fan-out          |
| acquire/renew dispatch lease | Revision-bound admission for upstream dispatch          |
| compare-and-transition       | Revision-checked lifecycle state changes                |
| revoke-and-drain leases      | Stop in-flight dispatch on delete or expiry             |
| expire-and-claim cleanup     | Grant exactly one cleanup owner for an expired record   |
| expiry-aware read            | Load a record without treating an expired one as usable |

Acquiring a lease verifies that the record is active, that its route and principal bindings match, and that its revision is current. It returns an opaque **lease fence** bound to that revision. Every upstream dispatch and reconnect verifies its fence, and long-lived GET streams stay registered to it.

Each mutation carries the record revision and must fail on a state or revision mismatch. A caller reloads and retries only when the requested operation is still valid.

The contract distinguishes these results, because they map to different client-visible outcomes: absent, expired, pending, closing/closed, invalid-schema, conflict, and temporary-store-unavailable.

A Redis-compatible backend such as Valkey is the first backend to evaluate after maintainers approve this contract. This proposal does not add a Valkey dependency or select a client library.

## Lifecycle, Deletion, and TTL

The record state machine is `pending → active → closing → closed`. Only `active` records can acquire or renew a dispatch lease and dispatch upstream traffic.

### Client DELETE

A client `DELETE` atomically performs a revision-checked `active → closing` transition, increments the revision, revokes every lease for the prior revision, and installs a short tombstone TTL.

Revocation cancels registered long-lived GET streams, prevents their reconnects and further event reads, and prevents an admitted-but-not-yet-dispatched request from dispatching when it rechecks its fence.

The deleting replica waits for revoked lease holders to drain before best-effort upstream cleanup, then atomically changes `closing → closed`. The tombstone remains until its TTL expires even when cleanup or the final transition fails. If a lease reaches its expiry or cannot be renewed, its holder stops dispatching and drains.

### TTL expiry

**An active-record TTL is an expiry deadline, not a store operation that deletes the active record.** Deleting the record on expiry would drop the upstream session IDs still needed to clean up upstreams.

Each replica's lifecycle reaper periodically scans an expiry index, and an acquire or renew operation also invokes `expire-and-claim-cleanup` when it observes a deadline in the past. That operation atomically checks the active revision and deadline, transitions the record to `closing`, increments the revision, revokes all prior leases, and grants exactly one short-lived cleanup-owner lease — while retaining the sensitive upstream entries under the closing tombstone.

The owner waits for revoked holders to drain, performs best-effort upstream cleanup, and changes the record to `closed`. If it crashes or its ownership lease expires, another reaper may atomically claim the still-closing tombstone and continue cleanup. No claimant may revive or dispatch the session.

Only after cleanup completes, or the tombstone-retention policy ends, may the store delete the record. TTL expiry therefore has the same client-visible result as `closed` and never restores the record. Every replica rejects `pending`, `closing`, and `closed` records, and no state transition or TTL-refresh operation can revive them.

## Admission and Dispatch

The active-record TTL begins at activation.

Before every external-session operation, the proxy validates the record and atomically acquires or renews its dispatch lease. If that admission step fails, it does not forward the request upstream. A lease holder rechecks its fence immediately before every upstream dispatch or reconnect, and stops when the fence is revoked or expires.

No record mutation is required _after_ upstream dispatch, so an already-dispatched event or request never creates an uncertain cursor or state-transition outcome.

External-mode sessions otherwise fail closed. The proxy must not forward a request to any upstream if the store cannot be reached, the record is absent or expired, its schema version is unsupported, its lifecycle state is not active, or its route or principal binding does not validate. Header-encoded sessions are unaffected by a store outage, because they do not use the store.

## Record Format and Rolling Compatibility

The record is a deterministic, bounded binary envelope owned by `internal/mcpproxy`: a fixed format version, canonical field encoding, explicit lengths, and a maximum total record size and per-field size.

Decoders reject malformed, oversized, or duplicate required fields. Unknown optional fields are ignored only when the envelope version declares them forward-compatible; unknown required fields and unsupported versions fail closed.

A writer may emit a new version only after all serving replicas can read it. During a rolling upgrade, new code must continue reading and writing the prior compatible version, or perform an explicit revision-checked migration. It must never overwrite an unreadable record. Implementation tests must exercise old-reader/new-writer and new-reader/old-writer overlap.

## Datastore Security Boundary

Upstream session IDs and the complete session record are sensitive, bearer-equivalent gateway-to-backend state. Anyone who can read the store can impersonate the gateway to every upstream in every live session.

A production backend must therefore provide:

- mutually authenticated, encrypted transport;
- datastore ACLs restricted to the gateway identity and this record namespace;
- integrity protection for records;
- encrypted, access-controlled backups and restores;
- audit logging;
- a rotation and revocation process.

Where the datastore does not provide authenticated encryption at rest with keys unavailable to datastore operators, the proxy must apply envelope encryption and integrity protection using keys managed outside the datastore, such as a KMS. Key identifiers, rotation compatibility, and failure behavior are part of the backend configuration contract.

Session IDs, principals, plaintext records, and encryption keys must not appear in logs, traces, metrics, or backup diagnostics.

## Error Mapping

External mode returns only stable, client-safe errors. Responses never include session IDs, resume tokens, principals, upstream identifiers, store error text, or upstream response bodies.

| Condition                                                        | Status                    | Retryable                                                      | Client action                                    |
| ---------------------------------------------------------------- | ------------------------- | -------------------------------------------------------------- | ------------------------------------------------ |
| Malformed, oversized, or unauthenticated session or resume token | `400 Bad Request`         | No                                                             | Fix the request                                  |
| Valid token whose route or principal binding fails               | `403 Forbidden`           | No                                                             | Re-authenticate                                  |
| Session absent, expired, pending, closing, or closed             | `404 Not Found`           | No                                                             | Establish a new session; do not retry the old ID |
| Unsupported record schema                                        | `409 Conflict`            | No                                                             | Establish a new compatible session               |
| Revision or lease conflict                                       | `409 Conflict`            | Only if the operation is idempotent and no result was received | Retry or re-establish                            |
| Temporary store outage                                           | `503 Service Unavailable` | Yes, with bounded guidance such as `Retry-After`               | Retry                                            |

Internal logs, traces, metrics, and diagnostics may record an approved low-cardinality outcome code, route, and backend count, but use the same redaction rules as client responses.

## Coexistence with Header-Encoded Sessions

External sessions are independent of header sessions. Enabling the mode affects newly initialized sessions only. Existing encrypted header sessions remain valid and continue through the current path until clients close them or stop using them.

The proxy does **not** silently convert a header session into an external record. Doing so would introduce persistence and identity-binding behavior without the client having explicitly created a session in that mode.

Operators migrate by enabling external mode for a controlled route or deployment and allowing clients to establish new sessions.

## Multi-Replica Operation

The implementation must be safe with multiple MCP Proxy replicas. Every replica uses the same store namespace and independently validates route and principal bindings before dispatching. Lifecycle transitions are revision-checked, dispatch leases are revision-fenced and drained, and reconnect tokens are client-acknowledged and stream-specific.

## Observability

Metrics should cover mode selection, record create/load/transition and lease outcomes, validation failures, TTL expiry, cleanup-owner recovery, transition conflicts, store latency, and store-unavailable failures. Traces should identify the route and backend count using approved low-cardinality attributes.

Logs must redact opaque IDs, upstream session IDs, resume tokens, principals, cookies, forwarded headers, datastore errors, and upstream response bodies.

## Required Test Coverage

End-to-end coverage must include:

- **Partial initialization** — only successful backends are stored, routed, and reflected in the aggregate response.
- **An event lost between proxy receipt and client delivery.**
- **Concurrent streams and replicas.**
- **A DELETE or expiry racing an admitted request and a long-lived GET** — proving neither can dispatch or reconnect after revocation, and both drain before cleanup.
- **Expiry detected both by an operation and by the lifecycle reaper**, including cleanup-owner crash recovery.
- **Pending-activation failure cleanup.**
- **Store outages at every lifecycle transition.**
- **A trusted principal assertion for initialize, GET, POST, and DELETE**, plus missing, ambiguous, and untrusted assertions.
- **GET-only `Last-Event-ID` handling** — proving POST and DELETE cannot affect a later notification GET.
- **Malformed, oversized, duplicate, and unknown-backend resume tokens**, and oversized upstream event IDs.
- **Principal/issuer/tenant mismatches, and anonymous requests.**
- **Stable error and retry mapping.**
- **Redaction** from client responses, logs, traces, metrics, and backup diagnostics.
- **Mixed-version record reads and writes.**

## Open Questions

These are for maintainers to weigh in on before any implementation work starts:

1. **Is the opt-in framing right?** This proposal assumes header-encoded sessions stay the default indefinitely. If maintainers would rather external state eventually become the default, the migration story needs to be designed now rather than deferred.
2. **How is the mode configured?** Per-`MCPRoute`, per-gateway, or install-wide. This proposal does not propose API surface, because the answer depends on whether operators are expected to mix modes within one gateway.
3. **Is the fail-closed store-outage behavior acceptable?** It means a store outage breaks all external-mode sessions. Fail-open is not viable for the reasons in [Admission and Dispatch](#admission-and-dispatch), so the alternative is accepting the availability coupling.
4. **Should the `AuthenticatedPrincipal` handoff be specified here or in a separate proposal?** It is a general trusted-hop mechanism that other features would likely want too.
