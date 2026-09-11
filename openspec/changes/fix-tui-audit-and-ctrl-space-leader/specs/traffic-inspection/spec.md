## Purpose

Keep traffic data and controls readable across terminal sizes, with clear response metadata, stable list columns, meaningful empty states, and explicit detail display modes.

## ADDED Requirements

### Requirement: Detail content reflows without loss
An open detail view SHALL rewrap after terminal-size or split-width changes. Content SHALL remain reachable without closing the view or receiving another network event. The view SHALL preserve the current logical section or nearest available scroll position.

#### Scenario: Narrow an open long response
- **WHEN** the operator narrows a 160-column terminal to 80 columns while viewing a long response
- **THEN** all displayed response content remains reachable by scrolling
- **AND** reopening the view is not necessary to recover missing line portions

### Requirement: Layout and controls fit the terminal
Pane content, borders, labels, and controls SHALL stay within the available terminal cells. At 80×24, detail and Repeater SHALL retain readable content plus back, help, and applicable send/cancel controls. Compact hints SHALL use effective bindings. If the available dimensions cannot fit a usable view, the application SHALL display a bounded resize message instead of overflowing.

#### Scenario: Narrow detail footer
- **WHEN** detail opens at 80×24
- **THEN** both pane borders and the status bar remain on-screen without an extra wrapped footer row

#### Scenario: Long Repeater result
- **WHEN** Repeater displays a long result or error at 80×24
- **THEN** the result does not displace the back or cancellation instructions

#### Scenario: Tiny terminal
- **WHEN** a terminal becomes too small for the current view
- **THEN** a resize message fits inside the available rows and columns
- **AND** the global quit action remains available

### Requirement: Response status is distinct from lifecycle
Detail SHALL display the HTTP status code and reason independently from capture lifecycle. It SHALL also retain request target, server identity, protocol, and existing error information. Streaming messages SHALL display one direction marker and any injection provenance.

#### Scenario: Completed server error
- **WHEN** an operator opens a completed HTTP 500 response
- **THEN** detail displays HTTP 500 and its reason alongside the separate completed lifecycle state

### Requirement: Traffic rows preserve comparison fields
Traffic rows SHALL reserve readable fields for lifecycle or HTTP status and response size before truncating descriptive text. Rows SHALL identify the actual capture protocol. Host identity and payload labels SHALL remain available through the selected-row summary or detail when compact columns cannot fit them. Keyboard selection, mouse hit-testing, and sorting SHALL refer to the same displayed rows.

#### Scenario: Long attack URL
- **WHEN** an attack result has a long URL and payload label at the default split
- **THEN** its status and response size remain visible
- **AND** the selected-row summary or detail exposes the host and complete payload label

#### Scenario: WebSocket handshake title
- **WHEN** a WebSocket flow has an HTTP/1.1 request-line title
- **THEN** the row identifies its actual protocol as WebSocket rather than implying an ordinary HTTP exchange

### Requirement: Empty states identify the reason
The traffic pane SHALL distinguish an empty capture, no matching filter results, and no flagged flows. Each state SHALL display the relevant counts and a next action using the effective keymap.

#### Scenario: Empty capture
- **WHEN** the capture contains no flows
- **THEN** the pane explains that no traffic has arrived and identifies how to use the target terminal

#### Scenario: No filter matches
- **WHEN** captured flows exist but the active filter matches none
- **THEN** the pane identifies the filter and displays how to clear or edit it

#### Scenario: No flagged flows
- **WHEN** flagged-only view contains no flows
- **THEN** the pane explains how to return to all flows or flag a flow

### Requirement: Detail modes identify transformations and limits
Detail SHALL expose a rebindable raw/expanded toggle, bound by default to `v`. Expanded mode SHALL retain the existing readable JSON and decoded-content display. Raw mode SHALL display the captured representation without JSON-string expansion and label its meaning per protocol. Neither mode SHALL mutate capture or export bytes. The view SHALL display scroll position and explicit size or message-count truncation information. Binary rows SHALL adapt to the available width without dangling ASCII fragments.

#### Scenario: Embedded JSON toggle
- **WHEN** an operator switches an embedded-JSON response from expanded to raw mode and back
- **THEN** the representations differ as labeled
- **AND** the stored message and subsequent export remain unchanged

#### Scenario: Preview limit
- **WHEN** content exceeds a display limit
- **THEN** detail identifies the omission and the action that exports the full stored content

#### Scenario: Narrow binary view
- **WHEN** the operator opens binary content in a narrow detail pane
- **THEN** each hex row fits its content width and the scroll indicator remains visible
