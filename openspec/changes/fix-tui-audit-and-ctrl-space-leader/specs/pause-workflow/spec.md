## Purpose

Make held traffic and its available actions explicit while preserving the operator's current input, drafts, and the identity of every queued pause.

## ADDED Requirements

### Requirement: Pause arrival preserves input ownership
A new pause SHALL NOT automatically switch keyboard focus, close a filter or command line, discard a draft, or replace an open editor. The interface SHALL notify the operator and provide a global action to inspect pending work. The default inspect-pause binding SHALL be leader then `p`.

#### Scenario: Pause while typing in the terminal
- **WHEN** a request pauses while the operator types in the hosted terminal
- **THEN** subsequent ordinary keys still reach the terminal
- **AND** a visible notification identifies pending work and the inspect-pause control

#### Scenario: Pause during a draft
- **WHEN** a message pauses while a filter, command line, or editor contains a draft
- **THEN** that draft and its input owner remain unchanged until the operator chooses another view

### Requirement: Pause controls identify their target
The pause display SHALL identify the held flow, request or response direction, and the number of additional queued pauses. For streaming traffic, it SHALL identify the held message within the flow. Edit, forward, and drop SHALL act on the visibly identified pause, not a different selected row. Direction labels and keyboard-focus labels SHALL remain understandable without color.

#### Scenario: Selected flow differs from active pause
- **WHEN** the traffic selection points to another flow while a pause is actionable
- **THEN** the action prompt explicitly identifies the held flow that edit, forward, or drop will affect

#### Scenario: Both directions on one connection
- **WHEN** two messages from the same connection pause in opposite directions
- **THEN** the interface distinguishes the messages and directions
- **AND** resolving one does not resolve the other

### Requirement: Queue promotion preserves pause identity
The application SHALL preserve arrival order for queued pauses and resolve each pause by its own identity. Resolving the active pause SHALL promote the next pause and update the displayed queue count. The next editor SHALL start with the next message's original content.

#### Scenario: Editor with another queued message
- **WHEN** message B pauses while message A has an open editor and the operator forwards A
- **THEN** B becomes the active pause with its own original content
- **AND** no draft or resolution intended for A applies to B

### Requirement: Inspecting a pause is deliberate and reversible
The inspect-pause action SHALL bring the identified pause controls into view without sending or dropping traffic. It SHALL preserve suspended drafts. Returning to the prior input context SHALL restore those drafts without an implicit submit.

#### Scenario: Inspect from a command draft
- **WHEN** the operator invokes inspect-pause from a partially typed command
- **THEN** the pause controls become visible without executing the command
- **AND** returning to the command restores its text and cursor
