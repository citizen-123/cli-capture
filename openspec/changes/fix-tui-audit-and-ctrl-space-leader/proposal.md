## Why

The live TUI audit exposed request corruption, hidden loaded sessions, blocked input, and incomplete WebSocket controls and capture history. These failures undermine traffic inspection and safe interception, so this change fixes every reported defect and UX gap together.

## What Changes

- Preserve HTTP headers, body bytes, and framing through intercepted-message editing and Repeater. Correct the CRLF-to-blank-line corruption and Content-Length repair.
- Display loaded sessions before new traffic arrives. Preserve saved flags and existing export formats.
- Run resend outside the UI update loop. Add cancellation, progress, and request-count previews to resend and Repeater jobs.
- Intercept incoming WebSocket data frames with the response toggle. Record successful injected frames in the message history and session exports.
- Rewrap open details after terminal or split changes. Fit detail and Repeater controls within the terminal, including 80×24 layouts.
- Display HTTP response status separately from capture lifecycle. Add scroll position and raw/expanded display controls with explicit truncation indicators.
- Use stable traffic columns for status, size, host, and protocol. Keep attack-result metadata accessible without hiding response size.
- Distinguish an empty capture from an empty filtered view. Display the next available action in each state.
- Display pause direction, queue depth, target identity, and keyboard focus. Preserve input focus and drafts when new pauses arrive.
- Generate all action hints from the effective keymap. Make leader commands available across detail, editor, filter, command, help, and Repeater contexts.
- **BREAKING:** Change the default leader from `Ctrl+A` to `Ctrl+Space`. Preserve explicit configuration and CLI overrides, including an explicitly selected `Ctrl+A`.
- **BREAKING:** Reserve the configured leader's runtime key identity across all modal bindings. Reject conflicting configurations instead of accepting unreachable actions.
- Update documentation, examples, screenshot-generation inputs, and behavioral coverage for the revised contracts.

## Capabilities

### New Capabilities

The project has no existing capability specifications. These new specifications define both corrected behavior and the requested additions.

- `http-message-editing`: Lossless HTTP editor boundaries, Repeater seeding, and explicit Content-Length repair.
- `tui-input-routing`: Ctrl+Space default, runtime aliases, global commands, keymap-derived hints, and preserved modal state.
- `capture-session-visibility`: Immediate preload visibility and session/export continuity.
- `websocket-interception`: Directional interception and recorded frame injection.
- `request-job-control`: Responsive resend, Repeater execution, planned counts, progress, cancellation, and result ownership.
- `traffic-inspection`: Responsive layouts, response metadata, traffic columns, empty states, and detail display modes.
- `pause-workflow`: Visible pause identity and queue state without automatic focus or draft loss.

### Modified Capabilities

None. `openspec list --specs` reports no existing specifications.

## Impact

The implementation affects `internal/tui`, `internal/replay`, `internal/repeater`, `internal/protocol`, `internal/intercept`, and capture metadata where necessary. CLI help, configuration validation, documentation, and screenshot tooling also change.

Context-aware network calls replace the existing internal resend/send contracts and migrate every caller. Existing session JSON remains readable. Any injection provenance metadata is additive and optional for older sessions.

The plan reuses Bubble Tea, the current terminal emulator, capture store, pause tokens, and streaming attack iterator. It adds no framework, external service, mandatory dependency, or privileged network configuration.

Ctrl+Space normally arrives as NUL (`ctrl+@` in Bubble Tea). Existing alias support remains in place. Terminals or multiplexers that reserve this chord require an explicit leader override.

This change contains planning artifacts only. Implementation requires a separate apply request.
