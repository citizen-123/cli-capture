# cli-capture — a visual tour

These images come from real PTY runs against local HTTP and WebSocket fixtures. All credentials and payloads are synthetic. Some views use a saved capture to exercise startup without new traffic.

The default global leader is **`Ctrl+Space`**. Press **`Ctrl+Space ?`** for help from any view. Explicit leader overrides remain available.

See [getting started](getting-started.md), [keybindings](keybindings.md), [configuration](configuration.md), [intercepting](intercepting.md), [Repeater](repeater.md), and [exporting](exporting.md) for the written guides.

## The split-pane monitor

The target runs in the left pane. The right pane shows captured traffic. Rows keep protocol, response status, and response size before clipping request text.

![Split-pane monitor](img/01-overview-split-pane.png)

## Request and response detail

Press Enter on a flow to open detail. The header separates HTTP response status from capture lifecycle. Expanded view formats and highlights JSON.

![Flow detail with JSON highlighting](img/02-flow-detail-json.png)

## Request headers and body

The same detail view shows the captured request, including headers and body.

![Request headers and body](img/03-request-headers-and-body.png)

## Decompression and raw display

Expanded view decodes `gzip`, `deflate`, `br`, and `zstd`. The original encoding header remains visible. Press `v` to switch to raw stored content.

![gzip response decoded](img/04-gzip-decoded-response.png)

## Filter the list

Press `/` to filter by host, method, path, status, or protocol. A zero-match view identifies the active filter instead of implying an empty capture.

![Filter by path](img/05-filter-by-path.png)

## Flag flows

Press Space to flag the selected flow. Flags survive a session save/load cycle.

![Flagged flows](img/06-flag-flows.png)

## Show only flagged flows

Press `F` to restrict the list to flagged flows.

![Flagged only](img/07-flagged-only.png)

## Sort results

Press `o` to cycle none, status, and size sorting. Status and byte-count columns remain readable at narrow widths.

![Sort by size](img/08-sort-by-size.png)

## Repeater with an inline response

Press `R` to open a request. Press `Ctrl+S` to start the job. `Tab` cycles request, payload, and response focus.

![Repeater with inline response](img/09-repeater-inline-response.png)

## Attack modes

`Ctrl+O` cycles single, sniper, battering-ram, pitchfork, and cluster-bomb. The planned count appears before execution. The job row reports actual progress.

![Repeater attack modes](img/10-repeater-attack-modes.png)

## A held request

Press `Ctrl+Space i` to arm requests, or `Ctrl+Space r` to arm responses. New pauses do not change keyboard focus. Press `Ctrl+Space p` to inspect the active held message.

![Intercepted request](img/11-intercept-paused.png)

## Edit without changing untouched bytes

Press `e` to edit the held message. An unchanged send preserves its original bytes. Header-only edits preserve its body. `Ctrl+R` explicitly replaces the body with text.

`Ctrl+L` repairs HTTP/1 `Content-Length`. Normal sends do not repair intentionally incorrect lengths. Escape closes the editor and leaves the message held.

![Edit intercepted request](img/12-intercept-edit-request.png)

## Effective help

`Ctrl+Space ?` opens help from editors, prompts, detail, terminal, and Repeater. The overlay displays configured bindings. Closing it restores the previous view.

![Help overlay](img/13-help-overlay.png)

## Export a curl command

Press `c` in traffic to export the selected request.

![Export flow as curl](img/14-export-flow-command.png)

## Export the session

`Ctrl+Space h` exports HAR. `Ctrl+Space s` saves session JSON, including complete stored bytes and WebSocket injection metadata.

![Export session as HAR](img/15-export-har.png)

## Cancel a request without blocking the interface

`Ctrl+Space c` cancels the active manual job from any view. `:cancel` works after the Repeater closes. The job row distinguishes cancellation from completion.

![Canceled resend](img/16-resend-canceled.png)

## WebSocket injection history

Successful injections appear once with direction and injection provenance. Session entries retain timestamps and opcodes. Server echoes remain separate messages. Closed-session and failed writes retain the editor draft instead of recording false success.

![WebSocket injection history](img/17-websocket-injection-history.png)

## Narrow detail at the bottom

Detail rewraps after a resize. A bottom-anchored view stays at the bottom, with the final content and footer visible at 80×24.

![Narrow detail with bottom anchor](img/18-detail-narrow-bottom.png)

## Queued pauses without color

The held-message row identifies its flow, direction, pause identity, and queued count. The selected row can differ from that target. These distinctions remain visible in the `none` theme.

![Queued pauses with a different selection](img/19-none-queued-pauses.png)

## Not pictured

The automated checks cover gRPC body editing and protocol byte preservation. Transparent Linux capture requires separate network setup and is outside this terminal audit.
