package protocol

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// GRPCMessage is one length-prefixed gRPC message unwrapped from a stream.
type GRPCMessage struct {
	Compressed bool
	Data       []byte
}

// EncodeGRPCFrames serializes messages into the gRPC wire format while
// preserving their order and per-message compression flag.
func EncodeGRPCFrames(messages []GRPCMessage) ([]byte, error) {
	var out bytes.Buffer
	for _, message := range messages {
		if err := writeGRPCFrame(&out, message); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

func writeGRPCFrame(w io.Writer, message GRPCMessage) error {
	if uint64(len(message.Data)) > uint64(^uint32(0)) {
		return fmt.Errorf("gRPC message is too large to frame: %d bytes", len(message.Data))
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
	var messages []GRPCMessage
	for len(body) > 0 {
		if len(body) < 5 {
			return nil, fmt.Errorf("incomplete gRPC frame header: %d trailing bytes", len(body))
		}
		if body[0] != 0 && body[0] != 1 {
			return nil, fmt.Errorf("unsupported gRPC compression flag %d", body[0])
		}
		length := uint64(binary.BigEndian.Uint32(body[1:5]))
		if length > uint64(len(body)-5) {
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
	buf       bytes.Buffer
}

// NewGRPCFramer wraps src so that every complete gRPC message read through it is
// reported to onMessage. The returned reader yields exactly src's bytes.
func NewGRPCFramer(src io.Reader, onMessage func(GRPCMessage)) io.Reader {
	return &grpcFramer{src: src, onMessage: onMessage}
}

func (g *grpcFramer) Read(p []byte) (int, error) {
	n, err := g.src.Read(p)
	if n > 0 {
		g.buf.Write(p[:n])
		g.drain()
	}
	return n, err
}

func (g *grpcFramer) drain() {
	for {
		if g.buf.Len() < 5 {
			return
		}
		length := int(binary.BigEndian.Uint32(g.buf.Bytes()[1:5]))
		if g.buf.Len() < 5+length {
			return // wait for the rest of this message
		}
		frame := make([]byte, 5+length)
		_, _ = io.ReadFull(&g.buf, frame)
		g.onMessage(GRPCMessage{Compressed: frame[0]&1 == 1, Data: frame[5:]})
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
	in        bytes.Buffer // raw input awaiting a complete message
	out       bytes.Buffer // re-framed output ready to serve
	srcErr    error
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
	buf := make([]byte, 32*1024)
	n, err := g.src.Read(buf)
	if n > 0 {
		g.in.Write(buf[:n])
		g.drain()
	}
	if err != nil {
		// Flush any trailing partial (malformed) bytes unchanged, then stop.
		if g.in.Len() > 0 {
			io.Copy(&g.out, &g.in)
		}
		g.srcErr = err
	}
}

func (g *grpcTamperReader) drain() {
	for {
		if g.in.Len() < 5 {
			return
		}
		length := int(binary.BigEndian.Uint32(g.in.Bytes()[1:5]))
		if g.in.Len() < 5+length {
			return
		}
		frame := make([]byte, 5+length)
		_, _ = io.ReadFull(&g.in, frame)
		msg := GRPCMessage{Compressed: frame[0]&1 == 1, Data: frame[5:]}
		out, drop := g.transform(msg)
		if drop {
			continue
		}
		if out == nil {
			out = msg.Data
		}
		_ = writeGRPCFrame(&g.out, GRPCMessage{Compressed: msg.Compressed, Data: out})
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
			i += 8
		case 2: // length-delimited
			l, m := binary.Uvarint(data[i:])
			if m <= 0 {
				return strings.Join(fields, " ")
			}
			i += m + int(l)
		case 5: // 32-bit
			i += 4
		default:
			return strings.Join(fields, " ") // group wire types / unknown: stop
		}
		fields = append(fields, fmt.Sprintf("#%d(%s)", fieldNum, wireName(wire)))
		if i > len(data) {
			break
		}
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
