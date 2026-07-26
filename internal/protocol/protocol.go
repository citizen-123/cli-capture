// Package protocol turns raw connection bytes into capture.Flow contents.
// Each application protocol is a Protocol implementation registered here;
// adding "another common protocol" means writing one file and one Register
// call, never touching the proxy or the UI.
package protocol

import (
	"bufio"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// Protocol parses one side of an intercepted connection. A Protocol first gets
// a chance to Detect whether it recognizes the stream, then Handle drives the
// bytes both ways, populating the Flow and forwarding upstream.
type Protocol interface {
	Name() capture.Protocol

	// Detect peeks at the first client bytes (without consuming them) and
	// reports whether this protocol should own the connection.
	Detect(peek []byte) bool

	// Handle owns the connection for the lifetime of the flow. It reads from
	// client, forwards to server, parses both directions into f, and calls
	// touch() whenever f changes so the UI updates. tamper is consulted before
	// each client→server message is forwarded (see intercept.Decider).
	Handle(f *capture.Flow, client, server *bufio.ReadWriter, tamper Tamperer, touch func()) error
}

// Tamperer is the hook the intercept engine implements. Protocols call it
// around each message; it may block (pausing the flow), mutate the bytes, or
// signal a drop. Kept as a narrow interface so protocol code has no dependency
// on the intercept package.
type Tamperer interface {
	// BeforeForward is called with a client→server message before it is sent
	// upstream. It returns the (possibly edited) bytes to forward, or drop=true
	// to abort the message.
	BeforeForward(f *capture.Flow, msg *capture.Message) (out []byte, drop bool)

	// BeforeDeliver is called with a server→client message before it is
	// delivered to the client. Same contract as BeforeForward, in the other
	// direction; drop=true withholds the response.
	BeforeDeliver(f *capture.Flow, msg *capture.Message) (out []byte, drop bool)

	// ShouldInterceptRequest / ShouldInterceptResponse report, without pausing,
	// whether a flow would be paused in that direction. Streaming transports
	// (HTTP/2) use these to decide whether to buffer a body for editing (only
	// when it will actually be intercepted) versus stream it through untouched.
	ShouldInterceptRequest(f *capture.Flow) bool
	ShouldInterceptResponse(f *capture.Flow) bool
}

// registry holds every known protocol, tried in registration order.
var registry []Protocol

// Register adds a protocol to the detection chain.
func Register(p Protocol) { registry = append(registry, p) }

// Detect returns the first registered protocol that claims peek, falling back
// to the raw-TCP passthrough which always matches.
func Detect(peek []byte) Protocol {
	for _, p := range registry {
		if p.Detect(peek) {
			return p
		}
	}
	return rawTCP{}
}
