## 1. Preserve HTTP editor bytes

- [x] 1.1 Add HTTP edit-buffer state in `internal/tui/editor.go` for original bytes, displayed baseline, and body boundaries (D01). Validate unchanged messages and header-only edits against exact body bytes.
- [x] 1.2 Use the edit-buffer state in intercepted request and response editors, including cancel/reopen behavior (D01). Validate an actual editor open/send path against a local endpoint with CRLF headers.
- [x] 1.3 Preserve binary and non-round-trippable bodies through header edits and explicit text replacement (D01). Validate that no textarea sanitizer silently changes retained bytes.
- [x] 1.4 Seed Repeater without doubled header newlines and retain existing logical-body reconstruction (D01). Validate that an unchanged captured GET reaches the origin with an empty body and correct headers.
- [x] 1.5 Make Content-Length repair use the reconstructed body boundary and byte count (D01). Validate UTF-8 lengths, duplicate-length prevention, and intentionally incorrect lengths without automatic repair.
- [x] 1.6 Extend existing editor/raw tests through the UI conversion boundary (D01). Validate multiline, compressed-body, and touched gRPC framing cases without weakening existing transport expectations.

## 2. Display loaded captures immediately

- [x] 2.1 Initialize the model's flow list from the preloaded store in `internal/tui/model.go` (D02). Validate that loaded flows appear before any new capture event.
- [x] 2.2 Exercise startup navigation, detail, filtering, flagged-only view, and export on loaded flows (D02). Validate saved flags and one new flow without duplication.
- [x] 2.3 Preserve documented export scope and error reporting during the preload change (D02). Validate session/HAR all-flow scope, flagged-only export content, and unwritable destinations.

## 3. Make request jobs responsive and cancellable

- [x] 3.1 Add context parameters to `replay.Resend` and `repeater.Send`, then migrate every caller (D03, U04). Use symbol references before changing signatures. Validate cancellation against a held local response.
- [x] 3.2 Replace synchronous resend dispatch with a model-owned asynchronous job and immutable request snapshot (D03). Validate that help and pane navigation respond while the server holds the response.
- [x] 3.3 Add bounded progress delivery, job identifiers, terminal outcomes, and one-active-manual-job enforcement (D03, U04). Validate duplicate-send prevention and completion/cancellation races without blocked producers.
- [x] 3.4 Add `job.cancel`, its default leader-`c` binding, and `:cancel` (D03, U04). Validate cancellation of active I/O, prevention of later attack requests, and retention of completed exchanges.
- [x] 3.5 Calculate Repeater request-count previews from the existing mode and payload-selection rules (U04). Validate counts 1/5/2/2/6, zero-position cases, and numeric overflow without materializing combinations.
- [x] 3.6 Report planned, attempted, succeeded, failed, and canceled outcomes for Repeater jobs (U04). Validate mixed responses/errors and payload-labeled exchange retention.
- [x] 3.7 Associate response updates with the originating job and Repeater session (U04). Validate close/reopen during a request, current edited-target titles, and replacement of stale response content.
- [x] 3.8 Preserve active-job feedback after Escape closes Repeater and cancel jobs during application shutdown (U04). Validate continued inspection, global cancellation, and termination without leaked job goroutines.

## 4. Complete WebSocket interception and capture history

- [x] 4.1 Route incoming WebSocket data frames through response interception in `internal/protocol/websocket.go` (D04). Validate response-only, request-only, both-enabled, and out-of-scope behavior with live peers.
- [x] 4.2 Support forward, edit, and drop for incoming frames without changing handshake or control-frame behavior (D04). Validate recipient payloads and continued connection use after a dropped frame.
- [x] 4.3 Record successful injections through the existing session writer and capture flow (D05). Validate exactly one marked entry per injection, correct direction/opcode/payload, and a separate ordinary echo.
- [x] 4.4 Keep failed injection drafts and report closed-session or write errors without false successful entries (D05). Validate a session that closes during composition and an incomplete write.
- [x] 4.5 Preserve optional injection metadata through session JSON and text exports (D05). Validate reload of injected messages and compatibility with sessions that lack the metadata.

## 5. Change the default leader and unify input routing

- [x] 5.1 Change `DefaultLeader` to `ctrl+space` while retaining current runtime alias support (L01). Validate NUL recognition, explicit CLI/file precedence, ordinary Ctrl+A passthrough, and one-byte double-leader passthrough.
- [x] 5.2 Dispatch leader commands before every modal input handler (D10, L01). Validate save, export, interception toggles, resize, mouse toggle, cancel-job, help, and quit without draft insertion.
- [x] 5.3 Add explicit foreground, suspended-view, and help-return state (D10). Validate terminal switching and help round-trips with intact draft text, cursor, and input ownership.
- [x] 5.4 Make pending leaders visible, Escape-cancellable, and safe for unknown commands (D10, L01). Validate no command-byte leakage to the child or text fields.
- [x] 5.5 Reject leader-runtime collisions in every context and migrate the modal-allowance test contract (D10, L01). Validate Ctrl+Space/Ctrl+@ equivalence, clear diagnostics, and distinct Alt-modified keys.
- [x] 5.6 Generate startup, status, footer, and help hints from the effective keymap (D09, L01). Validate Ctrl+G overrides, rebound editor controls, unbound actions, and the canonical Ctrl+Space default name.
- [x] 5.7 Align mouse dispatch with foreground and suspended views (D10). Validate that covered panes cannot steal focus and preserve existing hosted-terminal mouse translation and wheel-to-page behavior.

## 6. Preserve input during pause arrival

- [x] 6.1 Separate pause registration from focus activation in `internal/tui/model.go` (U02). Validate that terminal typing, filter text, command text, and open editor drafts survive new pauses.
- [x] 6.2 Add `pause.inspect` with default leader-`p` and reversible navigation through the foreground state (U01, U02). Validate inspection from a draft and return without an implicit submit or send.
- [x] 6.3 Display held-flow identity, direction, streaming-message identity, queued count, and textual keyboard focus (U01). Validate readability without color and correct prompts when the selected row differs from the active pause.
- [x] 6.4 Preserve FIFO promotion and token-specific resolution without focus transfer (U01, U02). Validate same-flow opposite-direction pauses and absence of draft leakage into the next editor.

## 7. Make traffic inspection readable at every supported size

- [x] 7.1 Recompute visible chrome and viewport geometry from terminal cell widths (D07). Validate complete detail and Repeater renders at 80×24, including long results, errors, and custom bindings.
- [x] 7.2 Rewrap open details on terminal and split changes while preserving a logical scroll anchor (D06). Validate shrink/grow without new traffic or reopening, including bottom-position retention.
- [x] 7.3 Add bounded compact-pane and tiny-terminal states (D07). Validate a terminal below 60×16, extreme splits, intact status placement, and available global quit.
- [x] 7.4 Display HTTP response code/reason separately from lifecycle and show streaming direction once (D08). Validate 404/500 details, existing errors, and injected-message markers.
- [x] 7.5 Reserve status and size columns and display actual protocol in traffic rows (U03). Validate long attack URLs, host/payload access, and consistent sort, selection, and mouse row mapping.
- [x] 7.6 Add distinct no-capture, no-filter-match, and no-flagged-flow states (U05). Validate counts and next-action hints under default and customized bindings.
- [x] 7.7 Add `detail.toggle-format`, default `v`, with labeled raw and expanded views (U06). Validate embedded JSON, decoded bodies, safe control-character display, and unchanged capture/export bytes.
- [x] 7.8 Add detail scroll position, explicit preview limits, and width-adaptive hex rows (U06). Validate long bodies, message-count caps, narrow binary content, and the full-content export action.

## 8. Validate integrated behavior in the real application

- [x] 8.1 Run the focused behavioral regressions for all seven capabilities after integration. Validate every D01–D10, U01–U06, and L01 ledger item against an observable outcome.
- [x] 8.2 Run `go build ./...`, `go vet ./...`, and `go test -race ./...` once after integration. Validate the project checks without weakening assertions to hide failures.
- [x] 8.3 Build and run the actual TUI in a PTY with isolated HTTP and WebSocket fixtures. Validate editor receipts, preload without traffic, directional pauses, injection history, and cancellable slow jobs.
- [x] 8.4 Exercise all five attack modes, command aliases, exports, and session reload end to end. Validate request combinations, outcome counts, exported content, and retained flags/provenance.
- [x] 8.5 Exercise NUL leader bytes, explicit leader overrides, all modal global actions, terminal passthrough, and shutdown. Validate no lost drafts, focus theft, command leakage, or background-job leaks.
- [x] 8.6 Exercise 160×48, 120×36, 80×24, tiny terminals, extreme splits, and all themes with keyboard and mouse. Validate readable bounds and retain terminal captures as evidence.

## 9. Update user guidance after the live acceptance pass

- [x] 9.1 Update `README.md` and the keybinding, configuration, and getting-started guides for Ctrl+Space and global modal behavior. Validate default examples, explicit Ctrl+A migration, and terminal/multiplexer override instructions against the effective keymap.
- [x] 9.2 Update interception, Repeater, export, and detail guidance for the completed behavior. Validate documented pause inspection, cancellation, counts, injection provenance, and raw/expanded controls against the running application.
- [x] 9.3 Update the screenshot procedure and affected images in `docs/screenshots.md` and `docs/img`. Validate that visible controls and layouts match the accepted build and contain only synthetic traffic.
- [x] 9.4 Add concise migration notes in the existing documentation and record final acceptance evidence. Validate that all 17 ledger items are accounted for and no project behavior remains represented by a placeholder.

## Acceptance evidence

All 51 tasks are complete. `verification.json` records all 17 ledger outcomes,
project checks, observed fixture results, and the 19 refreshed terminal images.
