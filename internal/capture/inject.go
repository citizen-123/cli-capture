package capture

// Injector pushes a frame into a live bidirectional session (currently
// WebSocket). Direction chooses which peer receives it; opcode and payload are
// protocol-specific (a WebSocket opcode and its bytes). Implemented by the
// protocol handler for an active connection.
type Injector interface {
	Inject(dir Direction, opcode byte, payload []byte) error
}

// SessionRegistrar is an optional capability a Tamperer may also implement.
// Streaming protocol handlers type-assert their tamper to it and register their
// live session so the UI can inject into it; a tamper that doesn't support
// injection simply won't satisfy the assertion.
type SessionRegistrar interface {
	RegisterSession(flowID string, inj Injector)
	UnregisterSession(flowID string)
}
