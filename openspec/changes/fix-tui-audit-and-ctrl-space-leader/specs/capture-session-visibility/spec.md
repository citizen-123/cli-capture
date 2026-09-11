## Purpose

Make saved captures immediately usable after loading, while preserving flags, captured content, and the established session and export behavior.

## ADDED Requirements

### Requirement: Loaded flows are visible at startup
The application SHALL display every successfully loaded flow on its first usable TUI render without requiring new network traffic. Navigation, detail, filters, flags, and exports SHALL operate on those flows immediately.

#### Scenario: Offline session inspection
- **WHEN** an operator loads a session containing 35 flows and the target sends no traffic
- **THEN** the traffic pane displays 35 flows
- **AND** the operator can inspect and export them immediately

#### Scenario: New traffic after preload
- **WHEN** one new flow arrives after a session containing 35 flows loads
- **THEN** the traffic pane contains 36 flows without duplicating preloaded entries

### Requirement: Save and load preserve capture meaning
Session save/load SHALL preserve flow identity, captured request and response content, message direction, and flags. Optional injection provenance SHALL survive the round-trip. Sessions without that metadata SHALL remain readable.

#### Scenario: Flagged-only view after reload
- **WHEN** a session containing two flagged flows loads and the operator enables flagged-only view
- **THEN** both flagged flows appear without any new capture event

#### Scenario: Older session compatibility
- **WHEN** a session without injection provenance loads
- **THEN** existing messages remain ordinary captured messages and no migration is required

### Requirement: Export behavior remains consistent
The application SHALL retain session JSON, HAR, selected-flow text, flagged-flow text, and curl exports. View filters SHALL NOT silently change the established export scope. Errors SHALL remain visible and SHALL NOT be reported as success.

#### Scenario: Filtered view and session export
- **WHEN** an operator saves a session or exports HAR while a view filter hides flows
- **THEN** the export retains its documented all-flow scope rather than only the visible subset

#### Scenario: Export destination failure
- **WHEN** an export destination cannot be written
- **THEN** the interface reports the failure and does not claim that an export completed
