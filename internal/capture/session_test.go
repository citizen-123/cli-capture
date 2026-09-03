package capture

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestSaveFileNarrowsPreexistingSession covers upgrade/reuse behavior: the
// mode passed to OpenFile applies only when it creates the file, so an existing
// session from a permissive version must be narrowed explicitly before new
// captured credentials are written.
func TestSaveFileNarrowsPreexistingSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owner-only POSIX modes are not expressible on Windows")
	}
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(path, []byte("[]\n"), 0o644); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod seed session: %v", err)
	}

	f := NewFlow("client:1", "api.example.com:443")
	f.Request = &Message{Body: []byte("Authorization: Bearer hunter2")}
	if err := SaveFile(path, []*Flow{f}); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat session: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("pre-existing session mode = %04o, want 0600", got)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("rewritten session contains %d flows, want 1", len(got))
	}
}

func TestSessionRoundTrip(t *testing.T) {
	f := NewFlow("client:1", "api.example.com:443")
	f.Protocol = ProtoHTTP1
	f.SNI = "api.example.com"
	f.Secure = true
	f.Status = StatusComplete
	f.Flagged = true
	f.Request = &Message{
		Direction: ClientToServer,
		Summary:   "GET /v1 HTTP/1.1",
		Body:      []byte("hello"),
		Raw:       []byte("GET /v1 HTTP/1.1\r\n\r\nhello"),
		Meta:      map[string]string{"method": "GET", "path": "/v1"},
	}
	f.Response = &Message{Direction: ServerToClient, Summary: "200 OK", Body: []byte("world")}

	var buf bytes.Buffer
	if err := Save(&buf, []*Flow{f}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(&buf)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 flow, got %d", len(got))
	}
	g := got[0]
	if g.ID != f.ID || g.SNI != f.SNI || g.Protocol != f.Protocol || !g.Secure || !g.Flagged {
		t.Errorf("flow scalars not preserved: %+v", g)
	}
	if g.Request == nil || string(g.Request.Body) != "hello" || g.Request.Meta["method"] != "GET" {
		t.Errorf("request not preserved: %+v", g.Request)
	}
	if g.Response == nil || string(g.Response.Body) != "world" {
		t.Errorf("response not preserved: %+v", g.Response)
	}
}

func TestSaveNilSessionLoadsAsEmptyArray(t *testing.T) {
	var buf bytes.Buffer
	if err := Save(&buf, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	flows, err := Load(&buf)
	if err != nil {
		t.Fatalf("Load saved empty session: %v", err)
	}
	if len(flows) != 0 {
		t.Fatalf("loaded %d flows, want none", len(flows))
	}
}

func TestLoadRejectsNullTraversalAndStructurallyInvalidFlows(t *testing.T) {
	tests := []struct {
		name    string
		session string
	}{
		{name: "null flow", session: `[null]`},
		{name: "null session", session: `null`},
		{name: "path traversal ID", session: `[{"ID":"../../outside","Protocol":"http/1.1","Status":2}]`},
		{name: "uppercase ID", session: `[{"ID":"0123456789AB","Protocol":"http/1.1","Status":2}]`},
		{name: "unknown protocol", session: `[{"ID":"0123456789ab","Protocol":"smtp","Status":2}]`},
		{name: "invalid request direction", session: `[{"ID":"0123456789ab","Protocol":"http/1.1","Status":2,"Request":{"Direction":1}}]`},
		{name: "null streaming message", session: `[{"ID":"0123456789ab","Protocol":"tcp","Status":2,"Messages":[null]}]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(strings.NewReader(tc.session)); err == nil {
				t.Fatal("Load accepted an invalid imported flow")
			}
		})
	}
}

func TestLoadRejectsRepresentationsAndMetadataBeyondBudgets(t *testing.T) {
	flow := NewFlow("client", "server:443")
	flow.Protocol = ProtoHTTP1
	flow.Request = &Message{
		Direction: ClientToServer,
		Body:      bytes.Repeat([]byte{'b'}, MaxRetainedLogicalBodyBytes+1),
	}
	var body bytes.Buffer
	if err := Save(&body, []*Flow{flow}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := Load(&body); err == nil {
		t.Fatal("Load accepted oversized decoded representation")
	}

	session := `[{"ID":"0123456789ab","Protocol":"http/1.1","Status":2,"Request":{"Direction":0,"Headers":{"X":["` +
		strings.Repeat("a", MaxRetainedHeaderBytes) + `"]}}}]`
	if _, err := Load(strings.NewReader(session)); err == nil {
		t.Fatal("Load accepted oversized headers")
	}
}

func TestDecodeSessionHeadersBoundsEmptyEntriesBeforeMapInsertion(t *testing.T) {
	headers, err := decodeSessionHeaders(json.NewDecoder(strings.NewReader(`{"":[]}`)))
	if err != nil {
		t.Fatalf("decode empty header: %v", err)
	}
	if values, ok := headers[""]; !ok || len(values) != 0 {
		t.Fatalf("empty header array = %#v, want retained empty entry", headers)
	}

	var input strings.Builder
	input.WriteByte('{')
	used := 0
	for i := 0; used <= MaxRetainedHeaderBytes; i++ {
		if i > 0 {
			input.WriteByte(',')
		}
		key := strconv.FormatInt(int64(i), 36)
		used += storageBytes(key)
		input.WriteString(`"`)
		input.WriteString(key)
		input.WriteString(`":[]`)
	}
	input.WriteByte('}')

	if _, err := decodeSessionHeaders(json.NewDecoder(strings.NewReader(input.String()))); err == nil {
		t.Fatal("decode accepted empty header entries beyond the retention budget")
	}
}

func TestSessionBudgetReaderRejectsOversizedJSONStringBeforeDecode(t *testing.T) {
	input := `"` + strings.Repeat("x", maxSessionJSONStringBytes()+1) + `"`
	reader := &sessionBudgetReader{
		r:         strings.NewReader(input),
		remaining: MaxSessionInputBytes,
		maxString: maxSessionJSONStringBytes(),
	}
	if _, err := io.ReadAll(reader); err == nil {
		t.Fatal("session reader accepted an oversized JSON string")
	}
}

func TestValidFlowIDAcceptsOnlyGeneratedFormat(t *testing.T) {
	for _, id := range []string{"0123456789ab", "deadbeefcafe"} {
		if !ValidFlowID(id) {
			t.Errorf("ValidFlowID(%q) = false, want true", id)
		}
	}
	for _, id := range []string{"", "0123456789a", "0123456789abc", "0123456789AB", "../deadbeef", "0123456789a/"} {
		if ValidFlowID(id) {
			t.Errorf("ValidFlowID(%q) = true, want false", id)
		}
	}
}
