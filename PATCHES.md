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
- Validated received parameters are an immutable, atomically published snapshot
  used by public state and migration. Stream registries and the DATAGRAM send
  queue own their effective policy, applied only at restoration or handshake
  application. There is no second connection-wide effective-parameter mirror.
- The DATAGRAM queue owns validation, payload copying, capacity and 0-RTT send
  generation under its existing mutex. Rejection clears queued early data and
  wakes all blocked producers with `Err0RTTRejected`; old producers cannot
  insert payloads after a new policy is applied. Accepted early data remains
  available before handshake completion. Normal non-0-RTT connections allocate
  no additional broadcast channel; blocked calls copy only upon admission.

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

`TestConnectionDatagram0RTTRejection` drives restored, received, rejected and
applied parameter phases through the connection, with a full send queue and
eight producers. It checks disabled and smaller new limits, rejection wakeup,
no old-data revival, and immutable copies of new payloads. The queue limit test
also checks packet-size limits and empty-frame header overhead.

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
