// Package capture defines the protocol-agnostic traffic model (Flow, Message)
// and a thread-safe Store that the UI subscribes to. Nothing in this package
// knows about HTTP, TLS, or bubbletea — it is the shared vocabulary the proxy,
// the protocol parsers, and the TUI all speak.
package capture

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Protocol names the application protocol a Flow was parsed as.
type Protocol string

const (
	ProtoHTTP1     Protocol = "http/1.1"
	ProtoHTTP2     Protocol = "http/2"
	ProtoWebSocket Protocol = "websocket"
	ProtoGRPC      Protocol = "grpc"
	ProtoRawTCP    Protocol = "tcp"
	ProtoUnknown   Protocol = "unknown"
)

// Direction distinguishes client→server bytes from server→client bytes. It is
// the only orientation concept streaming protocols (WebSocket, gRPC, raw TCP)
// need, and request/response protocols map cleanly onto it too.
type Direction int

const (
	ClientToServer Direction = iota
	ServerToClient
)

func (d Direction) String() string {
	if d == ClientToServer {
		return "→"
	}
	return "←"
}

// Status tracks a Flow through the intercept lifecycle.
type Status int

const (
	// StatusActive: bytes are flowing, nothing is paused.
	StatusActive Status = iota
	// StatusPending: matched an intercept rule and is paused awaiting a user
	// decision (forward / drop / edit).
	StatusPending
	// StatusComplete: the exchange finished normally.
	StatusComplete
	// StatusError: the connection or parse failed; see Flow.Err.
	StatusError
)

func (s Status) String() string {
	switch s {
	case StatusActive:
		return "active"
	case StatusPending:
		return "PAUSED"
	case StatusComplete:
		return "done"
	case StatusError:
		return "error"
	default:
		return "?"
	}
}

// Message is one parsed unit of traffic. For request/response protocols a Flow
// has one client Message (the request) and one server Message (the response).
// For streaming protocols a Flow accumulates many Messages in order.
type Message struct {
	Direction Direction
	Timestamp time.Time
	// Summary is the one-line human view, e.g. "GET /v1/users HTTP/1.1" or
	// "200 OK (1.2 KB)". Parsers fill this in.
	Summary string
	Headers map[string][]string
	Body    []byte
	// Raw is the full serialized message exactly as it went (or will go) on the
	// wire — request line + headers + body for HTTP, the frame payload for
	// streaming protocols. It is what the tamper editor seeds from and what an
	// edited Resolution.EditedBody replaces.
	Raw []byte
	// Truncated reports that Body or Raw exceeded its retention budget. A
	// truncated message is display-only and must not be replayed or edited as a
	// complete message.
	Truncated bool
	// Meta carries protocol-specific fields that don't fit Headers/Body
	// (gRPC status, WS opcode, HTTP method/path, …).
	Meta map[string]string
}

// Snapshot returns an independent deep copy of a message.
func (m *Message) Snapshot() *Message {
	return cloneMessage(m)
}

// Flow is one logical exchange over one connection.
type Flow struct {
	ID         string
	Protocol   Protocol
	ClientAddr string
	ServerAddr string // host:port as the client requested it
	SNI        string // TLS server name, empty for plaintext
	Secure     bool   // was this connection TLS-terminated by us?
	StartedAt  time.Time
	Status     Status
	Flagged    bool  // user-marked as interesting (UI-only; survives session save)
	Err        error `json:"-"` // an error interface can't round-trip through JSON

	// Request/Response for request-response protocols.
	Request  *Message
	Response *Message

	// Messages for streaming protocols (ordered). Guard appends with
	// AddMessage: streaming protocols pump both directions concurrently.
	Messages []*Message
	// Truncated reports that one or more messages or request/response bodies
	// could not be retained completely.
	Truncated bool
	mu        sync.RWMutex
}

// Mutate serializes changes to a live flow. Producers must complete related
// field changes in one callback before notifying the Store with Touch.
func (f *Flow) Mutate(fn func()) {
	if f == nil || fn == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// Snapshot returns an independent, deep copy that can safely outlive the
// producer. Maps, slices, and messages never share mutable storage with f.
func (f *Flow) Snapshot() *Flow {
	if f == nil {
		return nil
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return cloneFlow(f)
}

// AddMessage appends a streaming message under the flow lock. Messages beyond
// the count or aggregate-byte retention policy are not retained.
func (f *Flow) AddMessage(m *Message) {
	if m == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Messages) >= MaxMessagesPerFlow {
		f.Truncated = true
		return
	}
	f.Messages = append(f.Messages, m)
	limitFlowLocked(f)
}

// newID returns a short random hex id. crypto/rand keeps ids unpredictable so
// they can be used in log filenames without leaking a global counter.
func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// NewFlow constructs an active flow with a fresh id and start time.
func NewFlow(client, server string) *Flow {
	return &Flow{
		ID:         newID(),
		Protocol:   ProtoUnknown,
		ClientAddr: client,
		ServerAddr: server,
		StartedAt:  time.Now(),
		Status:     StatusActive,
	}
}

// Title is the compact label shown in the flow list.
func (f *Flow) Title() string {
	if f == nil {
		return ""
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	host := f.ServerAddr
	if f.SNI != "" {
		host = f.SNI
	}
	if f.Request != nil && f.Request.Summary != "" {
		return f.Request.Summary
	}
	return string(f.Protocol) + " " + host
}
