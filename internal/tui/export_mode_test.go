package tui

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/citizen-123/cli-capture/internal/capture"
)

func privateExportModel(dir string) (Model, *capture.Flow) {
	f := capture.NewFlow("client:1", "api.example.com:443")
	f.Protocol = capture.ProtoHTTP1
	f.Secure = true
	f.Flagged = true
	f.Request = &capture.Message{
		Headers: http.Header{"Authorization": {"Bearer hunter2"}},
		Body:    []byte("secret request"),
		Meta:    map[string]string{"method": "POST", "path": "/private"},
	}
	f.Response = &capture.Message{Body: []byte("secret response"), Meta: map[string]string{"status": "200 OK"}}
	store := capture.NewStore()
	store.Add(f)
	return Model{
		store: store, flows: []*capture.Flow{f}, viewFlow: f,
		fi: newFilter(), sessionPath: filepath.Join(dir, "session.json"),
	}, f
}

// TestExportsNarrowPreexistingFiles exercises every disk export through its
// real model method. os.WriteFile's 0600 argument does not affect an existing
// 0644 target; each path must explicitly narrow the opened file before writing
// intercepted headers and bodies into it.
func TestExportsNarrowPreexistingFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owner-only POSIX modes are not expressible on Windows")
	}
	tests := []struct {
		name   string
		path   func(string, *capture.Flow) string
		export func(Model) string
	}{
		{"HAR", func(dir string, _ *capture.Flow) string { return filepath.Join(dir, "capture.har") }, func(m Model) string { return m.exportHAR() }},
		{"viewed flow text", func(dir string, f *capture.Flow) string { return filepath.Join(dir, "flow-"+f.ID+".txt") }, func(m Model) string { return m.exportViewedFlow() }},
		{"flagged flow text", func(dir string, _ *capture.Flow) string { return filepath.Join(dir, "flagged.txt") }, func(m Model) string { return m.exportFlagged() }},
		{"curl", func(dir string, f *capture.Flow) string { return filepath.Join(dir, "flow-"+f.ID+".curl") }, func(m Model) string { return m.exportCurlSelected() }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			m, flow := privateExportModel(dir)
			path := tc.path(dir, flow)
			if err := os.WriteFile(path, []byte("stale permissive export"), 0o644); err != nil {
				t.Fatalf("seed export: %v", err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatalf("chmod seed export: %v", err)
			}

			if status := tc.export(m); strings.Contains(status, "failed") || strings.Contains(status, "error") {
				t.Fatalf("export status: %s", status)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat export: %v", err)
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Errorf("pre-existing export mode = %04o, want 0600", got)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read export: %v", err)
			}
			if string(body) == "stale permissive export" {
				t.Error("export did not replace stale contents")
			}
		})
	}
}
