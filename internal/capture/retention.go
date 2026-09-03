package capture

import (
	"errors"
	"sort"
)

// limitMessage normalizes all owned message fields before enforcing the
// per-message aggregate budget. Every retained slice and map is freshly
// allocated, so parser-owned buffers and net/http header maps cannot remain
// reachable from a capture.
func limitMessage(m *Message) bool {
	if m == nil {
		return false
	}
	truncated := false
	m.Body, truncated = copyBytesLimited(m.Body, MaxRetainedLogicalBodyBytes, truncated)
	m.Raw, truncated = copyBytesLimited(m.Raw, MaxRetainedMessageBytes, truncated)
	m.Headers, truncated = copyHeadersLimited(m.Headers, MaxRetainedHeaderBytes, truncated)
	m.Meta, truncated = copyMetaLimited(m.Meta, MaxRetainedMetaBytes, truncated)
	if len(m.Summary) > MaxRetainedHeaderBytes {
		m.Summary = m.Summary[:MaxRetainedHeaderBytes]
		truncated = true
	}
	return trimMessageToBudget(m, MaxRetainedMessageBudgetBytes, truncated)
}

func copyBytesLimited(in []byte, limit int, truncated bool) ([]byte, bool) {
	if len(in) > limit {
		in = in[:limit]
		truncated = true
	}
	if len(in) == 0 {
		return nil, truncated
	}
	return append([]byte(nil), in...), truncated
}

func copyHeadersLimited(in map[string][]string, limit int, truncated bool) (map[string][]string, bool) {
	if len(in) == 0 || limit <= 0 {
		if len(in) != 0 {
			truncated = true
		}
		return nil, truncated
	}
	keys := make([]string, 0, min(len(in), limit))
	for key := range in {
		if len(keys) == cap(keys) {
			truncated = true
			break
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string][]string, len(keys))
	used := 0
	for _, key := range keys {
		if used >= limit {
			truncated = true
			break
		}
		values := in[key]
		if len(values) == 0 {
			cost := storageBytes(key)
			if cost > limit-used {
				truncated = true
				continue
			}
			out[key] = nil
			used += cost
			continue
		}
		for _, value := range values {
			cost := storageBytes(key) + storageBytes(value)
			if cost > limit-used {
				truncated = true
				continue
			}
			out[key] = append(out[key], value)
			used += cost
		}
	}
	if len(out) == 0 {
		return nil, truncated
	}
	return out, truncated
}

func copyMetaLimited(in map[string]string, limit int, truncated bool) (map[string]string, bool) {
	if len(in) == 0 || limit <= 0 {
		if len(in) != 0 {
			truncated = true
		}
		return nil, truncated
	}
	keys := make([]string, 0, min(len(in), limit))
	for key := range in {
		if len(keys) == cap(keys) {
			truncated = true
			break
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(keys))
	used := 0
	for _, key := range keys {
		if used >= limit {
			truncated = true
			break
		}
		value := in[key]
		if storageBytes(key)+storageBytes(value) > limit-used {
			truncated = true
			continue
		}
		out[key] = value
		used += storageBytes(key) + storageBytes(value)
	}
	if len(out) == 0 {
		return nil, truncated
	}
	return out, truncated
}

func trimMessageToBudget(m *Message, budget int, truncated bool) bool {
	if m == nil {
		return truncated
	}
	if budget < 0 {
		budget = 0
	}
	if excess := messageBytes(m) - budget; excess > 0 {
		m.Raw, truncated = removeTail(m.Raw, excess, truncated)
	}
	if excess := messageBytes(m) - budget; excess > 0 {
		m.Body, truncated = removeTail(m.Body, excess, truncated)
	}
	if excess := messageBytes(m) - budget; excess > 0 {
		keep := headerBytes(m.Headers) - excess
		m.Headers, truncated = copyHeadersLimited(m.Headers, max(0, keep), true)
	}
	if excess := messageBytes(m) - budget; excess > 0 {
		keep := metaBytes(m.Meta) - excess
		m.Meta, truncated = copyMetaLimited(m.Meta, max(0, keep), true)
	}
	if excess := messageBytes(m) - budget; excess > 0 {
		m.Summary = m.Summary[:max(0, len(m.Summary)-excess)]
		truncated = true
	}
	if truncated {
		m.Truncated = true
	}
	return truncated || m.Truncated
}

func removeTail(in []byte, count int, truncated bool) ([]byte, bool) {
	if count <= 0 {
		return in, truncated
	}
	keep := max(0, len(in)-count)
	if keep == len(in) {
		return in, truncated
	}
	if keep == 0 {
		return nil, true
	}
	return append([]byte(nil), in[:keep]...), true
}

func messageBytes(m *Message) int {
	if m == nil {
		return 0
	}
	return len(m.Summary) + len(m.Body) + len(m.Raw) + headerBytes(m.Headers) + metaBytes(m.Meta)
}

func storageBytes(value string) int {
	return max(1, len(value))
}

func headerBytes(headers map[string][]string) int {
	n := 0
	for key, values := range headers {
		if len(values) == 0 {
			n += storageBytes(key)
			continue
		}
		for _, value := range values {
			n += storageBytes(key) + storageBytes(value)
		}
	}
	return n
}

func metaBytes(meta map[string]string) int {
	n := 0
	for key, value := range meta {
		n += storageBytes(key) + storageBytes(value)
	}
	return n
}

// limitFlow applies the retention policy at the Store boundary. Callers may
// construct Flow values directly (including from persisted sessions), so Store
// must not rely on protocol handlers to have bounded them already.
func limitFlow(f *Flow) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	limitFlowLocked(f)
}

func limitFlowLocked(f *Flow) {
	if f == nil {
		return
	}
	if trimFlowStatic(f) {
		f.Truncated = true
	}
	if len(f.Messages) > MaxMessagesPerFlow {
		f.Messages = append([]*Message(nil), f.Messages[:MaxMessagesPerFlow]...)
		f.Truncated = true
	}
	for _, m := range append([]*Message{f.Request, f.Response}, f.Messages...) {
		if limitMessage(m) {
			f.Truncated = true
		}
	}
	remaining := MaxRetainedFlowBytes - flowStaticBytes(f)
	for _, m := range append([]*Message{f.Request, f.Response}, f.Messages...) {
		if m == nil {
			continue
		}
		if trimMessageToBudget(m, max(0, remaining), false) {
			f.Truncated = true
		}
		remaining -= messageBytes(m)
	}
	if remaining < 0 {
		f.Truncated = true
	}
}

func trimFlowStatic(f *Flow) bool {
	truncated := false
	f.ID, truncated = truncateString(f.ID, MaxRetainedHeaderBytes, truncated)
	f.ClientAddr, truncated = truncateString(f.ClientAddr, MaxRetainedHeaderBytes, truncated)
	f.ServerAddr, truncated = truncateString(f.ServerAddr, MaxRetainedHeaderBytes, truncated)
	f.SNI, truncated = truncateString(f.SNI, MaxRetainedHeaderBytes, truncated)
	return truncated
}

func truncateString(value string, limit int, truncated bool) (string, bool) {
	if len(value) <= limit {
		return value, truncated
	}
	return value[:limit], true
}

func flowStaticBytes(f *Flow) int {
	return len(f.ID) + len(f.ClientAddr) + len(f.ServerAddr) + len(f.SNI)
}

func flowBytes(f *Flow) int {
	if f == nil {
		return 0
	}
	n := flowStaticBytes(f) + messageBytes(f.Request) + messageBytes(f.Response)
	for _, m := range f.Messages {
		n += messageBytes(m)
	}
	return n
}

func cloneFlow(f *Flow) *Flow {
	if f == nil {
		return nil
	}
	out := &Flow{
		ID:         f.ID,
		Protocol:   f.Protocol,
		ClientAddr: f.ClientAddr,
		ServerAddr: f.ServerAddr,
		SNI:        f.SNI,
		Secure:     f.Secure,
		StartedAt:  f.StartedAt,
		Status:     f.Status,
		Flagged:    f.Flagged,
		Truncated:  f.Truncated,
		Request:    cloneMessage(f.Request),
		Response:   cloneMessage(f.Response),
	}
	if f.Err != nil {
		out.Err = errors.New(f.Err.Error())
	}
	if len(f.Messages) != 0 {
		out.Messages = make([]*Message, len(f.Messages))
		for i, m := range f.Messages {
			out.Messages[i] = cloneMessage(m)
		}
	}
	return out
}

func cloneMessage(m *Message) *Message {
	if m == nil {
		return nil
	}
	out := *m
	out.Body = append([]byte(nil), m.Body...)
	out.Raw = append([]byte(nil), m.Raw...)
	out.Headers, _ = copyHeadersLimited(m.Headers, MaxRetainedHeaderBytes, false)
	out.Meta, _ = copyMetaLimited(m.Meta, MaxRetainedMetaBytes, false)
	return &out
}
