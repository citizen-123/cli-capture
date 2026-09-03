package protocol

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// GRPCMessage is one length-prefixed gRPC message unwrapped from a stream.
type GRPCMessage struct {
	Compressed bool
	Data       []byte
}

// EncodeGRPCFrames serializes messages into the gRPC wire format while
// preserving their order and per-message compression flag.
func EncodeGRPCFrames(messages []GRPCMessage) ([]byte, error) {
	if len(messages) > capture.MaxMessagesPerFlow {
		return nil, fmt.Errorf("too many gRPC messages: %d", len(messages))
	}
	total := 0
	for _, message := range messages {
		if len(message.Data) > capture.MaxFrameBytes {
			return nil, fmt.Errorf("gRPC message exceeds %d-byte limit", capture.MaxFrameBytes)
		}
		if total > capture.MaxRetainedWireBodyBytes-5-len(message.Data) {
			return nil, fmt.Errorf("gRPC body exceeds %d-byte limit", capture.MaxRetainedWireBodyBytes)
		}
		total += 5 + len(message.Data)
	}
	var out bytes.Buffer
	out.Grow(total)
	for _, message := range messages {
		if err := writeGRPCFrame(&out, message); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

func writeGRPCFrame(w io.Writer, message GRPCMessage) error {
	if len(message.Data) > capture.MaxFrameBytes {
		return fmt.Errorf("gRPC message exceeds %d-byte limit", capture.MaxFrameBytes)
	}
	var header [5]byte
	if message.Compressed {
		header[0] = 1
	}
	binary.BigEndian.PutUint32(header[1:], uint32(len(message.Data)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(message.Data)
	return err
}

// DecodeGRPCFrames parses a complete gRPC request body. Unlike the streaming
// observer, it rejects partial frames and invalid compression flags.
func DecodeGRPCFrames(body []byte) ([]GRPCMessage, error) {
	if len(body) > capture.MaxRetainedWireBodyBytes {
		return nil, fmt.Errorf("gRPC body exceeds %d-byte limit", capture.MaxRetainedWireBodyBytes)
	}
	var messages []GRPCMessage
	for len(body) > 0 {
		if len(messages) >= capture.MaxMessagesPerFlow {
			return nil, fmt.Errorf("too many gRPC messages")
		}
		if len(body) < 5 {
			return nil, fmt.Errorf("incomplete gRPC frame header: %d trailing bytes", len(body))
		}
		if body[0] != 0 && body[0] != 1 {
			return nil, fmt.Errorf("unsupported gRPC compression flag %d", body[0])
		}
		length := binary.BigEndian.Uint32(body[1:5])
		if length > capture.MaxFrameBytes {
			return nil, fmt.Errorf("gRPC frame exceeds %d-byte limit: %d", capture.MaxFrameBytes, length)
		}
		if int(length) > len(body)-5 {
			return nil, fmt.Errorf("incomplete gRPC frame payload: declared %d bytes, have %d", length, len(body)-5)
		}
		end := 5 + int(length)
		messages = append(messages, GRPCMessage{
			Compressed: body[0] == 1,
			Data:       append([]byte(nil), body[5:end]...),
		})
		body = body[end:]
	}
	return messages, nil
}

// grpcFramer is a pass-through io.Reader that parses gRPC length-prefixed
// messages out of the stream as they flow, calling onMessage for each complete
// message. It never alters the bytes — it only observes — so it is safe to
// splice into a proxied request or response body without breaking streaming.
//
// gRPC framing: [1-byte compressed flag][4-byte big-endian length][message].
type grpcFramer struct {
	src       io.Reader
	onMessage func(GRPCMessage)
	onError   func(error)
	buf       bytes.Buffer
	discard   uint64
}

// NewGRPCFramer wraps src so that every complete gRPC message read through it is
// reported to onMessage. The returned reader yields exactly src's bytes.
func NewGRPCFramer(src io.Reader, onMessage func(GRPCMessage)) io.Reader {
	return NewGRPCFramerWithError(src, onMessage, nil)
}

// NewGRPCFramerWithError is NewGRPCFramer with a notification for malformed or
// over-budget frames. Those bytes still pass through unchanged, but are never
// accumulated as an inspectable message.
func NewGRPCFramerWithError(src io.Reader, onMessage func(GRPCMessage), onError func(error)) io.Reader {
	return &grpcFramer{src: src, onMessage: onMessage, onError: onError}
}

func (g *grpcFramer) Read(p []byte) (int, error) {
	n, err := g.src.Read(p)
	if n > 0 {
		g.observe(p[:n])
	}
	return n, err
}

func (g *grpcFramer) observe(p []byte) {
	for len(p) > 0 {
		if g.discard > 0 {
			n := uint64(len(p))
			if n > g.discard {
				n = g.discard
			}
			p = p[n:]
			g.discard -= n
			continue
		}

		room := 5 + capture.MaxFrameBytes - g.buf.Len()
		if room <= 0 {
			g.report(fmt.Errorf("gRPC frame exceeds %d-byte limit", capture.MaxFrameBytes))
			g.buf.Reset()
			continue
		}
		n := len(p)
		if n > room {
			n = room
		}
		_, _ = g.buf.Write(p[:n])
		p = p[n:]
		g.drain()
	}
}

func (g *grpcFramer) drain() {
	for g.discard == 0 && g.buf.Len() >= 5 {
		header := g.buf.Bytes()
		flag := header[0]
		length := uint64(binary.BigEndian.Uint32(header[1:5]))
		if flag != 0 && flag != 1 {
			g.rejectCurrent(length, fmt.Errorf("unsupported gRPC compression flag %d", flag))
			return
		}
		if length > capture.MaxFrameBytes {
			g.rejectCurrent(length, fmt.Errorf("gRPC frame exceeds %d-byte limit: %d", capture.MaxFrameBytes, length))
			return
		}
		total := 5 + int(length)
		if g.buf.Len() < total {
			return
		}
		frame := g.buf.Next(total)
		if g.onMessage != nil {
			g.onMessage(GRPCMessage{
				Compressed: flag == 1,
				Data:       append([]byte(nil), frame[5:]...),
			})
		}
	}
}

func (g *grpcFramer) rejectCurrent(length uint64, err error) {
	payload := g.buf.Bytes()[5:]
	available := uint64(len(payload))
	var remainder []byte
	if available > length {
		remainder = append([]byte(nil), payload[length:]...)
		available = length
	}
	g.buf.Reset()
	g.discard = length - available
	g.report(err)
	if len(remainder) > 0 {
		g.observe(remainder)
	}
}

func (g *grpcFramer) report(err error) {
	if g.onError != nil {
		g.onError(err)
	}
}

// GRPCTransform is called for each complete gRPC message; it returns the
// (possibly edited) message bytes to forward, or drop=true to omit the message.
// A nil out means "forward unchanged".
type GRPCTransform func(GRPCMessage) (out []byte, drop bool)

// grpcTamperReader is like grpcFramer but it can rewrite the stream: each
// complete message is passed to transform, and the (edited) message is re-framed
// into the output. This lets a streaming gRPC call be tampered message-by-message
// without ever buffering the whole body — so bidi/long-lived streams don't stall.
type grpcTamperReader struct {
	src       io.Reader
	transform GRPCTransform
	in        bytes.Buffer // raw input awaiting one complete bounded message
	out       bytes.Buffer // re-framed output ready to serve
	srcErr    error
	readBuf   [32 * 1024]byte
}

// NewGRPCTamperReader wraps src so that each gRPC message is passed through
// transform and re-framed. The reader yields the transformed stream.
func NewGRPCTamperReader(src io.Reader, transform GRPCTransform) io.Reader {
	return &grpcTamperReader{src: src, transform: transform}
}

func (g *grpcTamperReader) Read(p []byte) (int, error) {
	for g.out.Len() == 0 && g.srcErr == nil {
		g.fill()
	}
	if g.out.Len() > 0 {
		return g.out.Read(p)
	}
	return 0, g.srcErr
}

func (g *grpcTamperReader) fill() {
	n, err := g.src.Read(g.readBuf[:])
	if n > 0 {
		_, _ = g.in.Write(g.readBuf[:n])
		if drainErr := g.drain(); drainErr != nil {
			g.in.Reset()
			g.srcErr = drainErr
			return
		}
	}
	if err != nil {
		if g.in.Len() > 0 {
			g.in.Reset()
			g.srcErr = fmt.Errorf("incomplete gRPC frame: %w", err)
			return
		}
		g.srcErr = err
	}
}

func (g *grpcTamperReader) drain() error {
	for {
		if g.in.Len() < 5 {
			return nil
		}
		header := g.in.Bytes()
		flag := header[0]
		if flag != 0 && flag != 1 {
			return fmt.Errorf("unsupported gRPC compression flag %d", flag)
		}
		length := binary.BigEndian.Uint32(header[1:5])
		if length > capture.MaxFrameBytes {
			return fmt.Errorf("gRPC frame exceeds %d-byte limit: %d", capture.MaxFrameBytes, length)
		}
		total := 5 + int(length)
		if g.in.Len() < total {
			return nil
		}
		frame := g.in.Next(total)
		msg := GRPCMessage{Compressed: flag == 1, Data: append([]byte(nil), frame[5:]...)}
		out, drop := g.transform(msg)
		if drop {
			continue
		}
		if out == nil {
			out = msg.Data
		}
		if err := writeGRPCFrame(&g.out, GRPCMessage{Compressed: msg.Compressed, Data: out}); err != nil {
			return err
		}
	}
}

// ProtoFieldSummary walks protobuf wire format and returns a compact,
// schema-less list of the top-level field numbers and wire types, e.g.
// "#1(len) #2(varint) #5(i64)". It is a readability aid — "what's in this
// message" without a .proto — not a decoder. Output is capped so a huge message
// can't flood the UI.
func ProtoFieldSummary(data []byte) string {
	const maxFields = 12
	var fields []string
	i := 0
	for i < len(data) && len(fields) < maxFields {
		tag, n := binary.Uvarint(data[i:])
		if n <= 0 {
			break
		}
		i += n
		fieldNum := tag >> 3
		wire := tag & 7
		switch wire {
		case 0: // varint
			_, m := binary.Uvarint(data[i:])
			if m <= 0 {
				return strings.Join(fields, " ")
			}
			i += m
		case 1: // 64-bit
			if len(data)-i < 8 {
				return strings.Join(fields, " ")
			}
			i += 8
		case 2: // length-delimited
			l, m := binary.Uvarint(data[i:])
			if m <= 0 {
				return strings.Join(fields, " ")
			}
			remaining := len(data) - i - m
			if remaining < 0 || l > uint64(remaining) {
				return strings.Join(fields, " ")
			}
			i += m + int(l)
		case 5: // 32-bit
			if len(data)-i < 4 {
				return strings.Join(fields, " ")
			}
			i += 4
		default:
			return strings.Join(fields, " ") // group wire types / unknown: stop
		}
		fields = append(fields, fmt.Sprintf("#%d(%s)", fieldNum, wireName(wire)))
	}
	return strings.Join(fields, " ")
}

func wireName(wire uint64) string {
	switch wire {
	case 0:
		return "varint"
	case 1:
		return "i64"
	case 2:
		return "len"
	case 5:
		return "i32"
	default:
		return "?"
	}
}
