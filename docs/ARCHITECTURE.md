# cli-capture — Architecture

**Goal:** Burp Suite, but for CLI / TUI applications. A split-pane terminal app:
the **left pane** hosts an interactive terminal running the *target* CLI; the
**right pane** monitors, proxies, and lets you tamper with that target's network
traffic mid-flight.

## The core problem

A web browser can be *told* to use a proxy. An arbitrary CLI cannot. So the
central engineering question is **transport interception**: how do we get an
unmodified third-party binary to route its bytes through us?

We separate two concerns that are easy to conflate:

1. **Transport capture** — *getting* the bytes off the wire.
2. **Protocol parsing** — *understanding* the bytes (HTTP, gRPC, WebSocket, …).

Keeping these orthogonal is what makes "all common protocols" a matter of
registering a parser, not rewriting the engine.

## Transport capture strategies (layered, most-portable first)

| Mode | Mechanism | Coverage | Privileges | Status |
|------|-----------|----------|------------|--------|
| `proxy-env` | Inject `HTTP_PROXY`/`HTTPS_PROXY`/`ALL_PROXY` + `SSL_CERT_FILE` into the child | Well-behaved HTTP(S) clients (curl, Go net/http, requests, …) | none | **implemented** |
| `socks` | Same, via a SOCKS5 listener (`ALL_PROXY=socks5://…`) | Anything that honors SOCKS | none | interface ready |
| `transparent` | Linux nftables REDIRECT → transparent proxy reading `SO_ORIGINAL_DST` | **All TCP**, protocol-agnostic, even apps that ignore env | `CAP_NET_ADMIN` | **implemented** (`internal/transparent` + `proxy.HandleTransparent`); privileged redirect untested in sandbox |

`proxy-env` is the default because it needs no privileges and covers the 80%
case. `transparent` mode is the honest answer to "all protocols" — it captures
raw TCP regardless of whether the app cooperates — and is isolated behind the
same `Interceptor` interface so it can land without touching the UI or parsers.

## TLS

Interception of TLS requires a **man-in-the-middle CA**. On first run we
generate a CA keypair (`~/.cli-capture/ca.pem`). For each intercepted TLS
connection we mint a leaf certificate for the requested SNI on the fly, signed
by our CA, and cache it. The child trusts our CA because we point `SSL_CERT_FILE`
/ `NODE_EXTRA_CA_CERTS` / etc. at it. Hosts can be allow/deny-listed for MITM vs.
blind passthrough — see `internal/intercept`.

## Component map

```
cmd/cli-capture         entrypoint, flags, wiring
internal/runner         PTY-hosted target process + env injection
internal/terminal       full VT emulator (vt10x) behind an Emulator interface
internal/proxy          the interception engine (HTTP CONNECT + MITM)
internal/proxy/http2.go HTTP/2 bridge (http2.Server + ReverseProxy) for h2/gRPC
internal/transparent    Linux SO_ORIGINAL_DST + nftables redirect (transparent mode)
internal/proxy/ca       CA generation + on-the-fly leaf signing
internal/protocol       byte-level parsers: http/1.1, websocket, tcp, grpc frames
internal/capture        Flow / Message model + thread-safe Store + session save/load
internal/intercept      pause/edit queue + scope rules  ← key policy
internal/scope          composable match engine (field × strategy × include/exclude)
internal/replay         resend a captured HTTP/gRPC flow to its origin
internal/tui            bubbletea split-pane UI + raw-bytes editor + leader keys
```

Two transport paths after TLS termination, chosen by ALPN:
- **`h2`** → `serveH2`: Go's `http2.Server` ⇄ `ReverseProxy` ⇄ `http2.Transport`,
  capturing at the request/response layer; gRPC frames parsed from the bodies.
- **anything else** → `bridge`: byte-level `Protocol.Handle` (HTTP/1, WebSocket
  upgrade, raw-TCP fallback).

## Data flow

```
target CLI ──stdin/stdout──▶  [PTY]  ──▶ terminal buffer ──▶ LEFT pane
     │
     └── socket ──▶ [proxy listener] ──▶ Interceptor
                          │
                          ├─ TLS? ──▶ ca.LeafFor(sni) ──▶ MITM
                          ├─ protocol.Detect(bytes) ──▶ parse into Flow
                          ├─ intercept.Rules match? ──▶ pause (StatusPending)
                          │        └─▶ user edits in RIGHT pane ──▶ resume
                          └─ forward upstream ──▶ capture.Store ──▶ RIGHT pane
```

## Concurrency model

- The proxy runs one goroutine per accepted connection.
- `capture.Store` is mutex-guarded and publishes change events on a channel.
- The bubbletea program consumes store events as `tea.Msg`s, so the UI never
  touches proxy internals directly (single-writer UI, classic Elm loop).
- The intercept queue blocks the *handling* goroutine (not the UI) on a
  per-flow channel until the user makes a decision.

## v1 protocol targets

HTTP/1.1 (implemented), HTTP/2 + gRPC, WebSocket, and raw-TCP passthrough as a
catch-all. Each is a `protocol.Protocol` implementation registered at startup.
