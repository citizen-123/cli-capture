package capture

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
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
