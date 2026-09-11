## Purpose

Make WebSocket interception respect direction controls and retain an accurate inspection history of frames that the operator injects into live connections.

## ADDED Requirements

### Requirement: WebSocket interception respects direction
Client-to-server data frames SHALL use request interception controls. Server-to-client data frames SHALL use response interception controls. Both directions SHALL respect the existing scope rules. Handshake and control frames SHALL retain their existing untampered behavior.

#### Scenario: Response-only interception
- **WHEN** response interception is enabled and an in-scope server sends a WebSocket data frame
- **THEN** the frame pauses before delivery to the client
- **AND** outgoing data frames continue unless request interception is also enabled

#### Scenario: Request-only interception
- **WHEN** request interception is enabled and an in-scope client sends a WebSocket data frame
- **THEN** the outgoing frame pauses and incoming frames continue without a response pause

#### Scenario: Edit or drop an incoming frame
- **WHEN** an operator edits or drops a paused incoming data frame
- **THEN** the client receives the edited payload or receives no dropped frame, respectively
- **AND** the connection can continue with later frames

#### Scenario: Scope excludes a frame
- **WHEN** a WebSocket flow is out of scope
- **THEN** neither direction pauses because of enabled interception controls

### Requirement: Successful injections enter capture history
Each successfully written injected frame SHALL appear exactly once in its flow's message history. The entry SHALL include direction, payload, opcode, timestamp, and explicit injection provenance. Session and text exports SHALL retain that information. An injected frame SHALL NOT re-enter interception as though it came from a peer.

#### Scenario: Inject in both directions
- **WHEN** an operator injects one frame toward the server and one toward the client
- **THEN** both peers receive their respective payloads
- **AND** detail contains two marked injection entries with the correct directions
- **AND** any server echo appears as a separate ordinary captured message

#### Scenario: Reload injected messages
- **WHEN** the operator saves and reloads a session containing injected frames
- **THEN** their directions, payloads, and injection markers remain visible

### Requirement: Injection failures are not successful capture entries
The application SHALL report a closed session or failed frame write without a success message. It SHALL NOT add a successful-injection entry for an incomplete write. The operator SHALL retain the composed payload after an injection failure.

#### Scenario: Session closes during composition
- **WHEN** the session closes before the operator sends a composed injection
- **THEN** the application reports that the session closed
- **AND** the draft remains available without a false successful-injection entry
