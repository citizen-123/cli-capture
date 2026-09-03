package capture

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/citizen-123/cli-capture/internal/ownerfile"
)

// Save writes flows to w as indented JSON — a capture session that can be
// reloaded later with Load.
func Save(w io.Writer, flows []*Flow) error {
	if flows == nil {
		flows = []*Flow{}
	}
	snapshots := make([]*Flow, 0, len(flows))
	for _, flow := range flows {
		snapshots = append(snapshots, flow.Snapshot())
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(snapshots)
}

// SaveFile writes a capture session to path. The session holds request and
// response bodies verbatim, credentials included, so on POSIX it is
// owner-only (0600) and atomically replaces any previous session.
func SaveFile(path string, flows []*Flow) error {
	return ownerfile.WriteFunc(path, func(w io.Writer) error {
		return Save(w, flows)
	})
}

// Load reads a bounded session incrementally. It never constructs an unbounded
// []Flow, header map, or decoded base64 representation before applying the
// persistence limits to that object.
func Load(r io.Reader) ([]*Flow, error) {
	dec := json.NewDecoder(&sessionBudgetReader{
		r:         r,
		remaining: MaxSessionInputBytes,
		maxString: maxSessionJSONStringBytes(),
	})
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok != json.Delim('[') {
		return nil, fmt.Errorf("session must be a JSON array")
	}

	flows := make([]*Flow, 0)
	seen := make(map[string]struct{})
	total := 0
	for dec.More() {
		if len(flows) >= MaxFlowsInStore {
			return nil, fmt.Errorf("session has more than %d flows", MaxFlowsInStore)
		}
		flow, err := decodeSessionFlow(dec)
		if err != nil {
			return nil, fmt.Errorf("session flow %d: %w", len(flows), err)
		}
		if err := validateLoadedFlow(flow); err != nil {
			return nil, fmt.Errorf("session flow %d: %w", len(flows), err)
		}
		if _, exists := seen[flow.ID]; exists {
			return nil, fmt.Errorf("session flow %d: duplicates id %q", len(flows), flow.ID)
		}
		seen[flow.ID] = struct{}{}
		if n := flowBytes(flow); n > MaxRetainedFlowBytes || total > MaxRetainedStoreBytes-n {
			return nil, fmt.Errorf("session flow %d exceeds retention budget", len(flows))
		} else {
			total += n
		}
		flows = append(flows, flow)
	}
	tok, err = dec.Token()
	if err != nil {
		return nil, err
	}
	if tok != json.Delim(']') {
		return nil, fmt.Errorf("session must end with a JSON array")
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("session contains trailing JSON")
		}
		return nil, err
	}
	return flows, nil
}

func maxSessionJSONStringBytes() int {
	// Base64 grows by four bytes per three input bytes. Raw is the largest
	// permitted string-bearing representation; the small margin covers quotes.
	return ((MaxRetainedMessageBytes + 2) / 3 * 4) + 2
}

// sessionBudgetReader stops a JSON token before encoding/json grows its scanner
// buffer beyond the largest valid base64 representation. It also caps total
// bytes before any graph allocation can occur.
type sessionBudgetReader struct {
	r         io.Reader
	remaining int
	maxString int
	inString  bool
	escaped   bool
	stringLen int
}

func (r *sessionBudgetReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, fmt.Errorf("session exceeds %d bytes", MaxSessionInputBytes)
	}
	if len(p) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.r.Read(p)
	for i := range p[:n] {
		r.remaining--
		b := p[i]
		if r.inString {
			if r.escaped {
				r.escaped = false
				r.stringLen++
			} else if b == '\\' {
				r.escaped = true
				r.stringLen++
			} else if b == '"' {
				r.inString = false
				r.stringLen = 0
			} else {
				r.stringLen++
			}
			if r.inString && r.stringLen > r.maxString {
				return i, fmt.Errorf("session string exceeds %d bytes", r.maxString)
			}
		} else if b == '"' {
			r.inString = true
			r.escaped = false
			r.stringLen = 0
		}
	}
	if r.remaining == 0 && err == nil {
		err = fmt.Errorf("session exceeds %d bytes", MaxSessionInputBytes)
	}
	return n, err
}

func decodeSessionFlow(dec *json.Decoder) (*Flow, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok != json.Delim('{') {
		return nil, fmt.Errorf("is not an object")
	}
	flow := &Flow{}
	for dec.More() {
		key, err := sessionKey(dec)
		if err != nil {
			return nil, err
		}
		switch key {
		case "ID":
			err = dec.Decode(&flow.ID)
		case "Protocol":
			err = dec.Decode(&flow.Protocol)
		case "ClientAddr":
			err = dec.Decode(&flow.ClientAddr)
		case "ServerAddr":
			err = dec.Decode(&flow.ServerAddr)
		case "SNI":
			err = dec.Decode(&flow.SNI)
		case "Secure":
			err = dec.Decode(&flow.Secure)
		case "StartedAt":
			err = dec.Decode(&flow.StartedAt)
		case "Status":
			err = dec.Decode(&flow.Status)
		case "Flagged":
			err = dec.Decode(&flow.Flagged)
		case "Request":
			flow.Request, err = decodeSessionMessage(dec)
		case "Response":
			flow.Response, err = decodeSessionMessage(dec)
		case "Messages":
			flow.Messages, err = decodeSessionMessages(dec)
		case "Truncated":
			err = dec.Decode(&flow.Truncated)
		default:
			return nil, fmt.Errorf("has unsupported field %q", key)
		}
		if err != nil {
			return nil, err
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("is not a complete object")
	}
	return flow, nil
}

func decodeSessionMessages(dec *json.Decoder) ([]*Message, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok == nil {
		return nil, nil
	}
	if tok != json.Delim('[') {
		return nil, fmt.Errorf("messages is not an array")
	}
	messages := make([]*Message, 0)
	total := 0
	for dec.More() {
		if len(messages) >= MaxMessagesPerFlow {
			return nil, fmt.Errorf("has more than %d messages", MaxMessagesPerFlow)
		}
		message, err := decodeSessionMessage(dec)
		if err != nil {
			return nil, err
		}
		if message == nil {
			return nil, fmt.Errorf("message %d is null", len(messages))
		}
		if n := messageBytes(message); n > MaxRetainedFlowBytes-total {
			return nil, fmt.Errorf("stream messages exceed flow retention budget")
		} else {
			total += n
		}
		messages = append(messages, message)
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim(']') {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("messages is not complete")
	}
	return messages, nil
}

func decodeSessionMessage(dec *json.Decoder) (*Message, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok == nil {
		return nil, nil
	}
	if tok != json.Delim('{') {
		return nil, fmt.Errorf("message is not an object")
	}
	message := &Message{}
	for dec.More() {
		key, err := sessionKey(dec)
		if err != nil {
			return nil, err
		}
		switch key {
		case "Direction":
			err = dec.Decode(&message.Direction)
		case "Timestamp":
			err = dec.Decode(&message.Timestamp)
		case "Summary":
			err = dec.Decode(&message.Summary)
		case "Headers":
			message.Headers, err = decodeSessionHeaders(dec)
		case "Body":
			err = dec.Decode(&message.Body)
			if err == nil && len(message.Body) > MaxRetainedLogicalBodyBytes {
				err = fmt.Errorf("body exceeds %d bytes", MaxRetainedLogicalBodyBytes)
			}
		case "Raw":
			err = dec.Decode(&message.Raw)
			if err == nil && len(message.Raw) > MaxRetainedMessageBytes {
				err = fmt.Errorf("raw exceeds %d bytes", MaxRetainedMessageBytes)
			}
		case "Truncated":
			err = dec.Decode(&message.Truncated)
		case "Meta":
			message.Meta, err = decodeSessionMeta(dec)
		default:
			return nil, fmt.Errorf("message has unsupported field %q", key)
		}
		if err != nil {
			return nil, err
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("message is not complete")
	}
	if len(message.Summary) > MaxRetainedHeaderBytes {
		return nil, fmt.Errorf("summary exceeds %d bytes", MaxRetainedHeaderBytes)
	}
	if messageBytes(message) > MaxRetainedMessageBudgetBytes {
		return nil, fmt.Errorf("exceeds message retention budget")
	}
	return message, nil
}

func decodeSessionHeaders(dec *json.Decoder) (map[string][]string, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok == nil {
		return nil, nil
	}
	if tok != json.Delim('{') {
		return nil, fmt.Errorf("headers is not an object")
	}
	headers := make(map[string][]string)
	used := 0
	for dec.More() {
		key, err := sessionKey(dec)
		if err != nil {
			return nil, err
		}
		tok, err := dec.Token()
		if err != nil || tok != json.Delim('[') {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("header %q is not an array", key)
		}

		values, exists := headers[key]
		if !exists {
			keyBytes := storageBytes(key)
			if keyBytes > MaxRetainedHeaderBytes-used {
				return nil, fmt.Errorf("headers exceed %d bytes", MaxRetainedHeaderBytes)
			}
			// Charge the key before adding it to the map. Empty header arrays
			// retain a map entry too, and untrusted input must not allocate an
			// unbounded number of those entries.
			headers[key] = nil
			used += keyBytes
		}
		for dec.More() {
			var value string
			if err := dec.Decode(&value); err != nil {
				return nil, err
			}
			cost := storageBytes(value)
			if len(values) > 0 {
				cost += storageBytes(key)
			}
			if cost > MaxRetainedHeaderBytes-used {
				return nil, fmt.Errorf("headers exceed %d bytes", MaxRetainedHeaderBytes)
			}
			values = append(values, value)
			headers[key] = values
			used += cost
		}
		if tok, err := dec.Token(); err != nil || tok != json.Delim(']') {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("header %q is not complete", key)
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("headers is not complete")
	}
	return headers, nil
}

func decodeSessionMeta(dec *json.Decoder) (map[string]string, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok == nil {
		return nil, nil
	}
	if tok != json.Delim('{') {
		return nil, fmt.Errorf("meta is not an object")
	}
	meta := make(map[string]string)
	used := 0
	for dec.More() {
		key, err := sessionKey(dec)
		if err != nil {
			return nil, err
		}
		var value string
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		if storageBytes(key)+storageBytes(value) > MaxRetainedMetaBytes-used {
			return nil, fmt.Errorf("meta exceeds %d bytes", MaxRetainedMetaBytes)
		}
		meta[key] = value
		used += storageBytes(key) + storageBytes(value)
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("meta is not complete")
	}
	return meta, nil
}

func sessionKey(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	key, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("object field is not a string")
	}
	return key, nil
}

// ValidFlowID reports whether id has the exact format generated by NewFlow.
// IDs satisfying this predicate are safe to include in an export filename.
func ValidFlowID(id string) bool {
	if len(id) != 12 {
		return false
	}
	for i := range len(id) {
		if (id[i] < '0' || id[i] > '9') && (id[i] < 'a' || id[i] > 'f') {
			return false
		}
	}
	return true
}

func validateLoadedFlow(flow *Flow) error {
	if flow == nil {
		return fmt.Errorf("is null")
	}
	if !ValidFlowID(flow.ID) {
		return fmt.Errorf("has invalid id %q", flow.ID)
	}
	switch flow.Protocol {
	case ProtoHTTP1, ProtoHTTP2, ProtoWebSocket, ProtoGRPC, ProtoRawTCP, ProtoUnknown:
	default:
		return fmt.Errorf("has invalid protocol %q", flow.Protocol)
	}
	switch flow.Status {
	case StatusActive, StatusPending, StatusComplete, StatusError:
	default:
		return fmt.Errorf("has invalid status %d", flow.Status)
	}
	if flow.Request != nil && flow.Request.Direction != ClientToServer {
		return fmt.Errorf("request has invalid direction %d", flow.Request.Direction)
	}
	if flow.Response != nil && flow.Response.Direction != ServerToClient {
		return fmt.Errorf("response has invalid direction %d", flow.Response.Direction)
	}
	for i, message := range flow.Messages {
		if message == nil {
			return fmt.Errorf("message %d is null", i)
		}
		if message.Direction != ClientToServer && message.Direction != ServerToClient {
			return fmt.Errorf("message %d has invalid direction %d", i, message.Direction)
		}
	}
	return nil
}

// LoadFile reads a capture session from path.
func LoadFile(path string) ([]*Flow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Load(f)
}
