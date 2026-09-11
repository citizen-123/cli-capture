## Context

See `proposal.md` for motivation and scope. This document defines implementation decisions, not authorization to implement them.

The audit exercised the built application through a PTY and compared HTTP and WebSocket receipts with TUI output. Findings apply to HTTP/1.1 and WebSocket paths. HTTP/2 and gRPC remain compatibility checks, not audited successes.

The TUI uses one Bubble Tea model, a capture store, tokenized interception, a hosted terminal emulator, and asynchronous Repeater commands. Relevant observations are:

- `internal/tui/model.go` loads its visible flow slice only after a store event. Startup already preloads the store before subscription.
- `model.go:enterEdit` and `repeater.go:openRepeater` pass CRLF text into `newEditor`. Textarea sanitization turns CRLF into two newlines.
- `model.go:resendSelected` calls `replay.Resend` synchronously. Repeater already defers work, but its results lack job ownership and cancellation.
- `protocol/websocket.go` passes no tamper handler to the incoming pump. Its injection writer does not record injected messages.
- `sizeDetail` changes viewport dimensions without rebuilding content. Fixed footer text can wrap beyond the allocated height.
- `detail.go` omits response status metadata because it assumes the title already contains that status.
- `activatePause` changes focus and closes text inputs. Current modal dispatch bypasses the global leader.
- The current keymap already recognizes `ctrl+space` and `ctrl+@` as the same runtime key. The default remains `ctrl+a`.

Existing specifications are absent. Existing code and documentation still define compatibility constraints. In particular, `TestNewKeyMapAllowsLeaderRuntimeKeyInModalContexts` expressly permits a behavior that global routing must replace.

### Audit coverage ledger

Each item from the audit has a specification and implementation task group. The identifiers below remain stable for review and acceptance.

| ID | Audit item | Capability | Task group |
|---|---|---|---|
| D01 | HTTP editor and Repeater corruption | http-message-editing | 1 |
| D02 | Loaded sessions remain invisible | capture-session-visibility | 2 |
| D03 | Resend blocks the TUI | request-job-control | 3 |
| D04 | Incoming WebSocket interception is absent | websocket-interception | 4 |
| D05 | Injected frames are missing from history | websocket-interception | 4 |
| D06 | Open detail loses content after shrinking | traffic-inspection | 7 |
| D07 | Detail and Repeater footers overflow | traffic-inspection | 7 |
| D08 | Detail omits HTTP response status | traffic-inspection | 7 |
| D09 | Custom leader hints show built-in keys | tui-input-routing | 5 |
| D10 | Global commands fail inside modals | tui-input-routing | 5 |
| U01 | Pause direction, queue, and target are unclear | pause-workflow | 6 |
| U02 | Pause arrival silently steals focus | pause-workflow | 6 |
| U03 | Traffic rows hide comparison fields | traffic-inspection | 7 |
| U04 | Repeater lacks count, progress, cancellation, and current-result feedback | request-job-control | 3 |
| U05 | Empty states give no next action | traffic-inspection | 7 |
| U06 | Detail lacks mode, position, and useful binary layout | traffic-inspection | 7 |
| L01 | Change the default leader to Ctrl+Space | tui-input-routing | 5 |

## Goals / Non-Goals

**Goals:**

- Keep existing capture and transport boundaries, with explicit ownership for editable bytes and background jobs.
- Keep all network waits outside the UI update loop.
- Preserve keyboard input, pause identity, and capture content during view transitions.
- Reuse the current keymap, viewport, store, attack iterator, and terminal emulator.
- Migrate every affected internal caller without deprecated wrappers or dual defaults.

**Non-Goals:**

- No frontend rewrite, new networking framework, or external service.
- No new attack mode, parallel attack engine, retry policy, or automatic high-volume traffic.
- No privileged transparent-network changes, HTTP/2 header editor, or new protobuf editor.
- No full binary hex editor. Binary display and preservation remain in scope.
- No broad redesign of capture storage or unrelated security hardening.

## Decisions

### 1. Preserve HTTP bytes at the editor boundary

Keep the existing textarea, but add a small HTTP edit-buffer state beside it. It retains the original serialized message, displayed baseline, header/body boundary, and original body bytes.

Normalize CRLF to LF only for editable HTTP header display. Never normalize an entire serialized message indiscriminately. The editor recognizes the real blank separator before text enters textarea sanitization.

An unchanged intercepted send returns the original serialized bytes. A header-only edit serializes edited header lines with CRLF and reuses the original body. For an intentionally edited text body, send the visible UTF-8 bytes without rewriting its internal line endings.

Binary or non-round-trippable bodies use a clearly marked preserved-body representation. Header edits cannot alter that retained body. An explicit replacement operation accepts a new text body and marks the replacement before send. This needs no binary-editing framework.

Repeater uses the same safe display conversion for `Template.Raw()`, then its existing parsing and transport reconstruction. Existing `CapturedRequestBody`, content-encoding, and gRPC framing rules remain authoritative. When only headers change, preserve original logical body bytes.

`fixContentLength` operates on the reconstructed message boundary and body bytes, not the textarea's sanitized header text. Repair one effective length value without duplicating it. Do not silently repair lengths on normal intercepted sends, because intentional malformed lengths are supported.

**Alternative rejected:** Global CRLF replacement on input or output. It can corrupt bodies and hide intentionally malformed framing.

**Affected files:** `internal/tui/editor.go`, `model.go`, `repeater.go`, `internal/repeater/raw.go`, and their behavioral tests.

### 2. Initialize visible capture state from the store

Set the model's initial flow slice from `store.List()` in `New`. Keep event-driven refresh for later changes. Preserve the existing startup order that subscribes before constructing the model.

Loaded sessions then work without a synthetic event, dummy request, or polling timer. Save/export scope remains unchanged.

**Alternative rejected:** Publish a fake capture event after loading. It hides the constructor invariant and leaves other prepopulated-model callers vulnerable.

**Affected files:** `internal/tui/model.go`, session-loading checks near `cmd/cli-capture/main.go`, and capture/session tests.

### 3. Use one cancellable manual job with explicit ownership

Introduce a small model-owned job state: identifier, originating Repeater session, kind, cancel function, counts, and terminal outcome. Permit one operator-triggered network job at a time. Live proxy traffic remains independent.

Change `replay.Resend` and `repeater.Send` to accept `context.Context` as their first argument. Bind every generated HTTP request to that context. Migrate all production and test callers rather than retaining compatibility wrappers.

Create the command and immutable request snapshot in the UI path. Run network calls inside `tea.Cmd`. Deliver progress and completion messages tagged with the job identifier. Update TUI state only on the model thread.

Keep one bounded progress channel per active job and a model command that waits for its messages. Coalesce intermediate progress under load. Deliver terminal outcomes reliably. Cancellation and shutdown must unblock the producer and waiter without goroutine leaks.

Add the global `job.cancel` action, default leader then `c`, plus `:cancel`. A repeated send while a job is active reports the active job instead of starting another. Escape closes Repeater but does not silently cancel an attack. The status bar keeps progress and cancellation available outside Repeater.

Count attack jobs arithmetically from the same selected positions and lists that feed `Attack.Jobs()`. Use checked integer arithmetic. Preserve current zero-position semantics: cluster-bomb and single yield one, the other modes yield zero. Do not materialize a Cartesian product for previews.

Track planned, attempted, succeeded, and failed counts separately, plus cancellation state. When failed exchanges are available, preserve them. Do not discard `Send` errors as the current attack loop does.

Update the response pane with the current job's latest result. When a new job starts, clear or explicitly label the previous result. Use the parsed edited template for the title. Ignore stale result messages for another Repeater session, but retain their exchanges in the store.

**Alternatives rejected:** Synchronous resend blocks the UI. Unbounded concurrent manual jobs add coordination and accidental-traffic risk. Materialized job lists waste memory.

**Affected files:** `internal/replay/replay.go`, `internal/repeater/send.go`, `attack.go`, `internal/tui/repeater.go`, `model.go`, `command.go`, and `keymap.go`.

### 4. Correct WebSocket direction and injection recording

Route outgoing data frames through request interception and incoming data frames through `BeforeDeliver`. Use the existing response-capable tamper interface rather than interpreting the request toggle for both directions. Preserve scope checks, token resolution, control-frame handling, and untampered handshakes.

Keep all frame writes behind the existing per-peer mutexes. Associate each live session with its capture flow and touch callback. After a complete successful injection write, append a message through `Flow.AddMessage` with `Meta["injected"] = "true"` and the existing opcode metadata.

Record before releasing the relevant write-order boundary. Publish updates without calling UI or interception code under writer locks. Preserve per-direction ordering without inventing a total wire order across both directions.

Do not route injected frames back into interception. A peer's echo remains a separate ordinary message. On a partial or failed write, display the error and do not append a successful-injection entry. Keep the composed draft after failure.

Use optional message metadata rather than a new session format. Existing JSON serialization already preserves metadata. Detail and text export display the injection marker. HAR retains its existing HTTP-oriented scope.

**Alternatives rejected:** UI-only history insertion misses ordering and other callers. A second injection transport bypasses the existing synchronization.

**Affected files:** `internal/protocol/websocket.go`, `internal/intercept/intercept.go`, `internal/tui/model.go`, `detail.go`, and relevant export rendering.

### 5. Make Ctrl+Space global without losing local context

Change `DefaultLeader` to `ctrl+space`. Reuse `controlKeyType` for NUL identity and aliases. Keep explicit CLI-over-file-over-default precedence. Do not treat Ctrl+A as another implicit leader.

Move leader recognition before modal dispatch in `Model.Update`. Keep one pending-leader state, visible in shared status chrome. Escape cancels it. Unknown commands consume the chord, preserve content, and display help guidance.

When the terminal owns input, the double-leader chord sends one literal control byte. In other contexts it changes no draft and explains that terminal focus is required. Alt-modified keys remain distinct.

Reserve the effective leader runtime identity in every keymap context. Replace the existing modal-collision allowance and update its test contract. Diagnostics identify the context and alias collision, not merely a preferred spelling.

Retain modal data separately from current input ownership. Use explicit foreground, suspended-view, and help-return state, not a general-purpose navigation framework. A temporary pause view retains the prior foreground state. A terminal switch suspends the current view and restores it on return.

| Global action | Behavior inside a modal |
|---|---|
| Save/export, interception toggles, resize, mouse toggle, cancel job | Run once and retain the modal, draft, and cursor. |
| Help | Cover the current view, then restore its exact input owner and state. |
| Pane switch | Suspend the modal, focus the terminal, and resume the modal on the next switch back. |
| Inspect pause (`leader p`) | Show pause controls and retain the originating draft. Escape returns to that draft. |
| Quit | Cancel active work and use the existing orderly shutdown path. |

Keep keyboard and mouse routing consistent with the visible foreground. A click behind a covering overlay cannot change its hidden input owner. Existing terminal mouse translation and wheel-to-page behavior remain unchanged.

Generate startup, status, footer, and help hints from `KeyMap.Keys` and `LeaderName`. Display Ctrl+Space by default while documenting its NUL/ctrl+@ alias. Omit unbound hints and use the actual command opener where relevant.

**Alternatives rejected:** A second hard-coded leader leaves ambiguous compatibility. Silently disabling modal bindings violates configuration validation. Closing editors on global commands loses drafts.

**Affected files:** `internal/tui/keymap.go`, `model.go`, `help.go`, `mouse.go`, `command.go`, and `cmd/cli-capture/main.go`.

### 6. Preserve focus when pauses arrive

Keep the active pause and FIFO queue, but separate pause registration from foreground activation. Arrival updates notifications without changing terminal focus, modal state, draft text, or the current motion.

Add a default global `pause.inspect` action at leader then `p`. In the traffic pane, display a persistent pause summary with flow identity, direction, message identity, and queued count. The prompt names the actual pause affected by edit, forward, or drop.

Use `PauseToken` for resolution, including concurrent messages from the same flow. Queue promotion updates the summary without taking focus. Canceling an editor returns to the held message, and the next editor starts with that message's original bytes.

Display a textual focus label as well as border styling. Do not rely on color or an abbreviated `PAUS` token alone.

**Alternative rejected:** Automatic focus transfer with a brighter border still converts terminal typing into capture actions. Preserving focus avoids that hazard.

**Affected files:** `internal/tui/model.go`, `mouse.go`, `rows.go`, `keymap.go`, and `pause_test.go`.

### 7. Compute layout from visible chrome and content width

Use shared geometry for rendering and mouse hit-testing. Account for borders, labels, footer, status, and selected-row summaries before assigning viewport height. Measure terminal cells with ANSI-aware functions, not rune count.

Use compact one-line modal controls at 80×24, with secondary controls available through global help. Result/error text gets a separate bounded region rather than prepending unlimited text to controls. A pane too narrow for content shows a compact placeholder. A terminal below 60×16 shows a bounded resize message with global quit available.

Rebuild open detail content after either a terminal resize or split adjustment. Preserve the nearest source-line or section anchor. When the viewport is already at the bottom, retain that position. Avoid a wrap cache unless profiling proves a need.

Render response status explicitly from response metadata or summary, independent of flow lifecycle. Display streaming direction once and include injection provenance. Add a compact scroll-position indicator.

Use structured traffic fields with status/lifecycle and size reserved before the flexible target field. Include actual protocol. At narrow widths, move host and full payload label into a selected-row summary or detail instead of losing comparison fields. Keep list selection, filtering, flagging, sorting, and mouse rows aligned with the revised geometry.

Add distinct empty messages for no capture, no filter matches, and no flags. Generate next-action hints from effective bindings. Do not change current filter cancellation semantics as part of this audit scope.

**Alternatives rejected:** Truncating a completed row hides response size. Clamping only viewport dimensions leaves stale line wrapping. Allowing footers to wrap implicitly breaks height accounting.

**Affected files:** `internal/tui/model.go`, `rows.go`, `detail.go`, `repeater.go`, `mouse.go`, and layout tests.

### 8. Add explicit detail display modes

Add `detail.toggle-format`, default `v`, without repurposing an existing action. Keep expanded mode as the default. Raw mode uses captured `Message.Raw` where available and a labeled fallback to the captured body where full serialized bytes are unavailable.

Label HTTP serialized data, decoded bodies, and WebSocket payloads accurately. Raw does not promise original packet framing for formats that never stored it. Escape control characters for safe display rather than executing them.

Preserve capture and export bytes across mode changes. When display limits apply, show omitted byte/message counts and the full-content export action. Adapt hex bytes-per-row to available width so the ASCII column stays on the same row.

**Alternative rejected:** Calling the existing expanded view raw conceals transformations and fails the inspection contract.

**Affected files:** `internal/tui/detail.go`, `model.go`, `keymap.go`, and existing export-mode tests.

### 9. Validate consumer behavior, then the real terminal

Use existing test files and fixtures for plausible failure modes. Extend the HTTP editor checks through an actual editor open/send path and an observing endpoint. Parser-only round trips did not expose textarea corruption.

Use a held local response to prove that resend returns control and cancellation reaches the request. Exercise all five attack modes and mixed failures. Use synchronized fixtures instead of arbitrary sleep-based test assertions.

Validate initial preload without publishing a new event. Exercise paired WebSocket directions, successful injection history, closed-session failures, and concurrent queue identity.

Replace tests that encode the old modal-leader allowance. Remove wording-only assertions encountered in affected tests instead of pinning new wording. Retain behavioral checks for effective bindings, input ownership, bounds, and rendered metadata.

Finally, run the built TUI in a PTY with local HTTP/WebSocket fixtures. Cover 160×48, 120×36, 80×24, a tiny terminal, extreme split ratios, all themes, NUL leader delivery, explicit overrides, exports, and shutdown. Preserve endpoint receipts and terminal captures as acceptance evidence. Do not require a permanent UI automation framework.

## Risks / Trade-offs

- **Ctrl+Space is intercepted by a terminal or multiplexer** → Document an explicit leader override. Do not install a second implicit default.
- **Global routing invalidates existing modal bindings** → Fail configuration clearly and document the required rebind before release.
- **Text normalization changes body bytes** → Retain source bytes separately and exercise no-op, header-only, binary, UTF-8, and multiline cases.
- **Cancellation races with completion** → Use job identity and one terminal outcome. Retain completed exchanges and distinguish cancellation from transport failure.
- **Injection writes race with capture reads** → Use current writer and message-append synchronization. Exercise race-sensitive capture/export paths during focused validation.
- **Preserved focus makes pauses less immediate** → Keep persistent queue notifications and a global inspect-pause action.
- **Compact layouts reveal less text per row** → Preserve comparison columns and expose full metadata in the selected-row summary or detail.
- **Old captures lack raw data or injection metadata** → Label the available representation and treat absent provenance as ordinary capture.
- **HTTP/2 and gRPC lack audit coverage** → Keep existing framing tests. Validate compatibility where the editor or job changes touch them.

## Migration Plan

1. Implement each task group in dependency order, retaining the existing libraries and session format.
2. Migrate context-aware request APIs and all callers in one cutover.
3. Change the default and modal key validation together with generated hints and updated configuration guidance.
4. Preserve explicit leader configuration, including `-leader ctrl+a`, without rewriting user files.
5. Update `README.md`, `docs/keybindings.md`, `configuration.md`, `getting-started.md`, `intercepting.md`, `repeater.md`, `exporting.md`, and `screenshots.md` where contracts change.
6. Update the documented screenshot capture procedure and refresh affected images after a successful live smoke run. No existing screenshot generator was found.
7. Add release migration notes to the existing documentation because the repository has no changelog file.
8. Run the focused regressions and final terminal acceptance pass before release.

A code rollback restores the previous executable. The optional injection metadata requires no data migration. Explicit `-leader ctrl+a` restores familiar controls without a rollback. Keep a session copy before comparing releases.
