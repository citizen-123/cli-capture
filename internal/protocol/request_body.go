package protocol

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/citizen-123/cli-capture/internal/capture"
)

// RequestBody is the logical representation retained by capture. HTTP bodies
// are decoded for display; gRPC messages are unframed but retain the payload
// bytes and compression flag that appeared on the wire.
type RequestBody struct {
	Body     []byte
	Messages []GRPCMessage
}

const (
	// BodyRepresentationMeta distinguishes logical display bytes from legacy
	// synthetic captures that stored already encoded wire bytes in Body.
	BodyRepresentationMeta        = "body-representation"
	BodyRepresentationDecoded     = "decoded"
	BodyRepresentationUnavailable = "unavailable"
)

// CapturedRequestBody extracts a replayable logical request body from a flow.
// It validates raw bytes when capture retained them and refuses incomplete or
// contradictory representations rather than guessing at bytes to send.
func CapturedRequestBody(f *capture.Flow) (RequestBody, error) {
	if f == nil || f.Request == nil {
		return RequestBody{}, fmt.Errorf("flow has no captured request body")
	}

	switch f.Protocol {
	case capture.ProtoHTTP1:
		body, err := capturedHTTP1Body(f.Request)
		return RequestBody{Body: body}, err
	case capture.ProtoHTTP2:
		body, err := capturedHTTP2Body(f.Request)
		return RequestBody{Body: body}, err
	case capture.ProtoGRPC:
		return capturedGRPCBody(f)
	default:
		return RequestBody{}, fmt.Errorf("%s request bodies cannot be reconstructed", f.Protocol)
	}
}

// EncodeRequestBody is the authoritative logical-to-wire conversion used by
// replay and repeater. HTTP Content-Encoding is applied last, after any caller
// edits or template substitution; gRPC messages are framed first.
func EncodeRequestBody(proto capture.Protocol, header http.Header, body RequestBody) ([]byte, error) {
	var logical []byte
	switch proto {
	case capture.ProtoHTTP1, capture.ProtoHTTP2:
		if len(body.Messages) != 0 {
			return nil, fmt.Errorf("%s request unexpectedly contains gRPC messages", proto)
		}
		logical = body.Body
	case capture.ProtoGRPC:
		if len(body.Body) != 0 {
			return nil, fmt.Errorf("gRPC request contains an unframed body")
		}
		if err := validateGRPCCompression(header, body.Messages); err != nil {
			return nil, err
		}
		framed, err := EncodeGRPCFrames(body.Messages)
		if err != nil {
			return nil, err
		}
		logical = framed
	default:
		return nil, fmt.Errorf("%s request bodies cannot be encoded", proto)
	}

	encoding := requestContentEncoding(header)
	encoded, err := capture.EncodeContentEncoding(logical, encoding)
	if err != nil {
		return nil, err
	}

	return encoded, nil
}

func capturedHTTP1Body(message *capture.Message) ([]byte, error) {
	if message.Meta != nil && message.Meta[BodyRepresentationMeta] == BodyRepresentationUnavailable {
		return nil, fmt.Errorf("captured HTTP/1 request body is unavailable")
	}
	if len(message.Raw) == 0 {
		return capturedBodyWithoutRaw(message)
	}

	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(message.Raw)))
	if err != nil {
		return nil, fmt.Errorf("parse captured HTTP/1 request: %w", err)
	}
	wire, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("read captured HTTP/1 request body: %w", err)
	}
	encoding := requestContentEncoding(req.Header)
	storedEncoding := requestContentEncoding(messageHeader(message))
	if canonicalContentEncoding(encoding) != canonicalContentEncoding(storedEncoding) {
		return nil, fmt.Errorf("captured HTTP/1 Content-Encoding does not match its raw headers")
	}
	logical, err := capture.DecodeContentEncodingStrict(wire, encoding)
	if err != nil {
		return nil, fmt.Errorf("decode captured HTTP/1 request body: %w", err)
	}
	// DumpRequest preserves chunk framing in the captured display Body, while
	// ReadRequest above de-chunks Raw. Accept either that historical display
	// representation or the decoded logical bytes, but reject unrelated data.
	if hasChunkedTransferEncoding(req.TransferEncoding) {
		rawEntity := rawHTTP1Entity(message.Raw)
		if bytes.Equal(message.Body, logical) || bytes.Equal(message.Body, rawEntity) {
			return append([]byte(nil), logical...), nil
		}
		return nil, fmt.Errorf("captured chunked HTTP/1 body does not match its wire bytes")
	}

	// DecodeContentEncoding deliberately leaves a successfully decoded empty
	// entity as raw bytes for display. The strict result is authoritative here.
	if len(logical) == 0 && bytes.Equal(message.Body, wire) {
		return logical, nil
	}
	if len(message.Body) != 0 && !bytes.Equal(message.Body, logical) {
		return nil, fmt.Errorf("captured HTTP/1 logical body does not match its wire bytes")
	}
	return append([]byte(nil), logical...), nil
}

func hasChunkedTransferEncoding(encodings []string) bool {
	for _, encoding := range encodings {
		if strings.EqualFold(encoding, "chunked") {
			return true
		}
	}
	return false
}

func rawHTTP1Entity(raw []byte) []byte {
	if index := bytes.Index(raw, []byte("\r\n\r\n")); index >= 0 {
		return raw[index+4:]
	}
	return nil
}

func capturedHTTP2Body(message *capture.Message) ([]byte, error) {
	representation := ""
	if message.Meta != nil {
		representation = message.Meta[BodyRepresentationMeta]
	}
	if representation == BodyRepresentationUnavailable {
		return nil, fmt.Errorf("captured HTTP/2 request body is unavailable")
	}
	if representation == BodyRepresentationDecoded && len(message.Raw) != 0 && !isHTTP2HeaderDump(message.Raw) {
		encoding := requestContentEncoding(messageHeader(message))
		logical, err := capture.DecodeContentEncodingStrict(message.Raw, encoding)
		if err != nil {
			return nil, fmt.Errorf("decode captured HTTP/2 request body: %w", err)
		}
		if !bytes.Equal(logical, message.Body) {
			return nil, fmt.Errorf("captured HTTP/2 logical body does not match its wire bytes")
		}
		return append([]byte(nil), logical...), nil
	}
	if len(message.Body) == 0 {
		if representation != BodyRepresentationDecoded && isHTTP2HeaderDump(message.Raw) {
			return nil, fmt.Errorf("captured HTTP/2 request body is unavailable")
		}
		if representation == "" && len(message.Raw) == 0 {
			if err := validateCapturedContentEncoding(message); err != nil {
				return nil, err
			}
			if err := validateAbsentBody(messageHeader(message)); err != nil {
				return nil, err
			}
			return nil, nil
		}
		return capturedBodyWithoutRaw(message)
	}
	if len(message.Raw) != 0 && bytes.Equal(message.Raw, message.Body) {
		encoding := requestContentEncoding(messageHeader(message))
		logical, err := capture.DecodeContentEncodingStrict(message.Raw, encoding)
		if err != nil {
			return nil, fmt.Errorf("decode captured HTTP/2 request body: %w", err)
		}
		return append([]byte(nil), logical...), nil
	}
	if err := validateCapturedContentEncoding(message); err != nil {
		return nil, err
	}
	if encodedBodyIsAmbiguous(message) {
		return nil, fmt.Errorf("captured HTTP/2 body representation is unavailable for Content-Encoding %q", requestContentEncoding(messageHeader(message)))
	}
	return append([]byte(nil), message.Body...), nil
}

func isHTTP2HeaderDump(raw []byte) bool {
	return bytes.Contains(raw, []byte(" HTTP/2\r\n"))
}

func capturedBodyWithoutRaw(message *capture.Message) ([]byte, error) {
	if err := validateCapturedContentEncoding(message); err != nil {
		return nil, err
	}
	if encodedBodyIsAmbiguous(message) {
		return nil, fmt.Errorf("captured body representation is unavailable for Content-Encoding %q", requestContentEncoding(messageHeader(message)))
	}
	if len(message.Body) != 0 {
		return append([]byte(nil), message.Body...), nil
	}
	if err := validateAbsentBody(messageHeader(message)); err != nil {
		return nil, err
	}
	return nil, nil
}

func encodedBodyIsAmbiguous(message *capture.Message) bool {
	encoding := requestContentEncoding(messageHeader(message))
	if !hasNonIdentityContentEncoding(encoding) {
		return false
	}
	return message.Meta == nil || message.Meta[BodyRepresentationMeta] != BodyRepresentationDecoded
}

func validateCapturedContentEncoding(message *capture.Message) error {
	encoding := requestContentEncoding(messageHeader(message))
	if _, err := capture.EncodeContentEncoding(nil, encoding); err != nil {
		return fmt.Errorf("captured request body: %w", err)
	}
	return nil
}

func canonicalContentEncoding(header string) string {
	parts := strings.Split(header, ",")
	for i, part := range parts {
		parts[i] = strings.ToLower(strings.TrimSpace(part))
	}
	return strings.Join(parts, ",")
}

func requestContentEncoding(header http.Header) string {
	return strings.Join(header.Values("Content-Encoding"), ",")
}

func capturedGRPCBody(f *capture.Flow) (RequestBody, error) {
	header := messageHeader(f.Request)
	outerEncoding := requestContentEncoding(header)

	representation := ""
	if f.Request.Meta != nil {
		representation = f.Request.Meta[BodyRepresentationMeta]
	}
	if representation == BodyRepresentationUnavailable {
		return RequestBody{}, fmt.Errorf("captured gRPC request messages are unavailable")
	}

	messages := make([]GRPCMessage, 0, len(f.Messages))
	for _, message := range f.Messages {
		if message == nil || message.Direction != capture.ClientToServer {
			continue
		}
		compressed, err := capturedCompressionFlag(message)
		if err != nil {
			return RequestBody{}, err
		}
		if len(message.Raw) != 0 && !bytes.Equal(message.Raw, message.Body) {
			return RequestBody{}, fmt.Errorf("captured gRPC message body does not match its payload bytes")
		}
		messages = append(messages, GRPCMessage{
			Compressed: compressed,
			Data:       append([]byte(nil), message.Body...),
		})
	}

	if representation == BodyRepresentationDecoded && len(f.Request.Body) != 0 {
		if len(f.Request.Raw) != 0 && !bytes.Equal(f.Request.Raw, f.Request.Body) {
			return RequestBody{}, fmt.Errorf("captured gRPC framed body does not match its wire bytes")
		}
		parsed, err := decodeCapturedGRPCWire(f.Request.Body, outerEncoding)
		if err != nil {
			return RequestBody{}, err
		}
		if len(messages) != 0 && !grpcMessagesEqual(messages, parsed) {
			return RequestBody{}, fmt.Errorf("captured gRPC messages do not match the framed request body")
		}
		messages = parsed
	} else if len(messages) == 0 && len(f.Request.Body) != 0 {
		parsed, err := decodeCapturedGRPCWire(f.Request.Body, outerEncoding)
		if err != nil {
			return RequestBody{}, err
		}
		messages = parsed
	}

	if len(messages) == 0 {
		if representation != BodyRepresentationDecoded && isHTTP2HeaderDump(f.Request.Raw) {
			return RequestBody{}, fmt.Errorf("captured gRPC request messages are unavailable")
		}
		if err := validateAbsentBody(header); err != nil {
			return RequestBody{}, fmt.Errorf("gRPC request messages are unavailable: %w", err)
		}
	}
	if err := validateGRPCCompression(header, messages); err != nil {
		return RequestBody{}, err
	}
	return RequestBody{Messages: messages}, nil
}

func grpcMessagesEqual(left, right []GRPCMessage) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Compressed != right[i].Compressed || !bytes.Equal(left[i].Data, right[i].Data) {
			return false
		}
	}
	return true
}

func decodeCapturedGRPCWire(wire []byte, contentEncoding string) ([]GRPCMessage, error) {
	framed, err := capture.DecodeContentEncodingStrict(wire, contentEncoding)
	if err != nil {
		return nil, fmt.Errorf("decode captured gRPC Content-Encoding: %w", err)
	}
	messages, err := DecodeGRPCFrames(framed)
	if err != nil {
		return nil, fmt.Errorf("decode captured gRPC request body: %w", err)
	}
	return messages, nil
}

func hasNonIdentityContentEncoding(header string) bool {
	for _, part := range strings.Split(header, ",") {
		encoding := strings.TrimSpace(part)
		if encoding != "" && !strings.EqualFold(encoding, "identity") {
			return true
		}
	}
	return false
}

func capturedCompressionFlag(message *capture.Message) (bool, error) {
	value := ""
	if message.Meta != nil {
		value = strings.TrimSpace(message.Meta["compressed"])
	}
	switch strings.ToLower(value) {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, fmt.Errorf("unsupported captured gRPC compression flag %q", value)
	}
}

func validateGRPCCompression(header http.Header, messages []GRPCMessage) error {
	for _, message := range messages {
		if !message.Compressed {
			continue
		}
		encoding := strings.TrimSpace(header.Get("Grpc-Encoding"))
		if encoding == "" || strings.EqualFold(encoding, "identity") {
			return fmt.Errorf("compressed gRPC message has no usable Grpc-Encoding")
		}
	}
	return nil
}

func validateAbsentBody(header http.Header) error {
	if value := strings.TrimSpace(header.Get("Content-Length")); value != "" {
		length, err := strconv.ParseInt(value, 10, 64)
		if err != nil || length < 0 {
			return fmt.Errorf("invalid captured Content-Length %q", value)
		}
		if length > 0 {
			return fmt.Errorf("captured body is missing despite Content-Length %d", length)
		}
	}
	if value := strings.TrimSpace(header.Get("Transfer-Encoding")); value != "" && !strings.EqualFold(value, "identity") {
		return fmt.Errorf("captured body is missing despite Transfer-Encoding %q", value)
	}
	return nil
}

func messageHeader(message *capture.Message) http.Header {
	if message == nil {
		return nil
	}
	return http.Header(message.Headers)
}
