## Purpose

Preserve HTTP message content through the interactive editors so that inspection and intentional edits cannot silently change unrelated headers or body bytes.

## ADDED Requirements

### Requirement: Opening an editor preserves message content
The application SHALL preserve message content when an operator opens and sends an unchanged intercepted HTTP request or response. Repeater SHALL preserve the request method, target, header values, and logical body through its normal transport reconstruction. Editor initialization SHALL NOT introduce extra header separators.

#### Scenario: Unchanged GET in Repeater
- **WHEN** an operator opens a captured GET with an empty body in Repeater and sends it unchanged
- **THEN** the origin receives the captured method, target, and header values subject to existing transport-owned framing rules
- **AND** the origin receives an empty body, not the header text

#### Scenario: Unchanged intercepted messages
- **WHEN** an operator opens and sends an unchanged intercepted HTTP request or response containing CRLF headers
- **THEN** the recipient receives the original serialized message without added blank lines or body changes

### Requirement: Edits preserve unrelated bytes
The application SHALL limit changes to the operator's edits and explicitly requested framing repair. A header-only edit SHALL preserve the original body bytes, including CRLF and non-text bytes. Text conversion SHALL NOT silently discard unsupported bytes. The editor SHALL identify content that it cannot safely edit and preserve that content unless the operator explicitly replaces it.

#### Scenario: Header edit with a multiline body
- **WHEN** an operator changes one header in a message with a multiline body
- **THEN** the edited header reaches the recipient
- **AND** all original body bytes and unrelated header values remain unchanged

#### Scenario: Non-text content in an editor
- **WHEN** an operator opens a message whose body cannot round-trip through a text editor
- **THEN** the interface identifies the body as non-text and does not silently sanitize it
- **AND** an unchanged send or header-only edit preserves the original body bytes

### Requirement: Content-Length repair is explicit and byte accurate
The Content-Length action SHALL measure the actual body after the true header/body separator. It SHALL use byte length rather than character count. It SHALL NOT interpret header text as body content or add a second effective length header. Ordinary intercepted sends SHALL NOT silently repair a deliberately incorrect Content-Length.

#### Scenario: Edited UTF-8 body
- **WHEN** an operator changes a text body to multibyte UTF-8 content and requests Content-Length repair
- **THEN** the effective Content-Length equals the encoded body length in bytes
- **AND** no original header becomes part of the body

#### Scenario: Intentional malformed length
- **WHEN** an operator sends an intercepted message with an intentionally incorrect Content-Length without requesting repair
- **THEN** the application preserves that intentional value

### Requirement: Editor cancellation does not resolve a pause
The application SHALL leave the same message paused when the operator cancels its editor. A later editor or queued pause SHALL NOT inherit the canceled draft.

#### Scenario: Cancel and reopen
- **WHEN** an operator modifies a paused message, cancels its editor, and reopens that editor
- **THEN** the message remains paused and the editor starts from its original captured content
