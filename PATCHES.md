# Stream parameter ownership

Upstream: `quic-go/quic-go` v0.61.0, commit
`579ee19d5b54c4f9320ffca668113c3513a138e5`. The upstream MIT license is retained.
This bounded adaptation addresses the known concurrent 0-RTT stream creation
defect described in <https://github.com/quic-go/quic-go/issues/4303>.

## Ownership

- Each stream registry owns its effective initial send window and negotiated
  reset capability under the same mutex that creates and registers streams.
  Applying parameters updates existing streams and future creation together.
  No new connection-wide lock or handshake wait is added to opening a stream.
- Flow-controller construction receives the effective window explicitly; it
  never reads transport parameters being replaced by the handshake goroutine.
- Incoming bidirectional streams created by 0.5-RTT data receive the local
  bidirectional window when parameters apply. Already larger MAX_STREAM_DATA
  windows remain larger. Incoming unidirectional streams have no send policy.
- Rejecting 0-RTT retires the prior registries. New registries start with zero
  windows and disabled reset capability; old references cannot gain new policy.
- Received and effective transport parameters are immutable, atomically
  published snapshots with different protocol activation times. Public state
  and migration inspect validated received parameters; application datagrams
  use the effective snapshot. Stream parameters apply only at restoration or
  handshake application, preserving early-data semantics.

No exported API changes, protocol fallback, compatibility shim, unsafe access
or duplicated transport implementation is introduced.

## Verification

Run both uninstrumented and race-instrumented suites:

```sh
go test -p 1 -count=1 -timeout=5m ./...
go test -race -p 1 -count=1 -timeout=5m ./...
```

`TestStreamParametersRemainEffectiveUntilApplied` covers pending versus
effective windows, existing incoming streams before and after Accept, larger
MAX_STREAM_DATA and reset negotiation. The concurrent test covers simultaneous
bidirectional and unidirectional opening and parameter publication. Existing
reset/rejection and full protocol integration tests remain required.

The native zero-allocation frame-parser assertion lives in a `!race` test file:
Go's race runtime intentionally discards random `sync.Pool.Put` entries, making
zero pool allocations an invalid expectation in that mode. Its native assertion
is retained unchanged; parser correctness still runs in both suites. Do not
interpret allocation or timing measurements under `-race` as production cost.

Compare upstream `BenchmarkHandshake` and `BenchmarkStreamChurn` at the frozen
baseline and the candidate, using identical compiler, host and commands. Real
consumer DoQ/HTTP3 race checks and the HyperCacheDNS release gate remain separate
requirements. Workspace substitutions are development evidence only: final
dnsproxy and product modules must resolve the same immutable reviewed revision.
