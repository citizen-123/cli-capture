package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write drops a config file into dir and returns its path.
func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// configHome points XDG_CONFIG_HOME at a temp dir and returns the resulting
// cli-capture config directory.
func configHome(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDirFollowsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	if got, want := Dir(), filepath.Join("/tmp/xdg", "cli-capture"); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
	if got, want := DefaultPath(), filepath.Join("/tmp/xdg", "cli-capture", "config.json"); got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestResolvePresetsAndPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	cases := map[string]string{
		"work":             filepath.Join("/tmp/xdg", "cli-capture", "work.json"),
		"dark":             filepath.Join("/tmp/xdg", "cli-capture", "dark.json"),
		"./local.json":     "./local.json",
		"/etc/cc.json":     "/etc/cc.json",
		"sub/dir/cfg.json": "sub/dir/cfg.json",
		"relative.json":    "relative.json",
	}
	for spec, want := range cases {
		if got := Resolve(spec); got != want {
			t.Errorf("Resolve(%q) = %q, want %q", spec, got, want)
		}
	}
}

func TestPathsFlagAccumulates(t *testing.T) {
	var p Paths
	// Repeating the flag and comma-separating must both work — the bug this
	// type exists to avoid is a second -config silently discarding the first.
	_ = p.Set("base,dark")
	_ = p.Set("work")
	if got, want := strings.Join(p, ","), "base,dark,work"; got != want {
		t.Errorf("Paths = %q, want %q", got, want)
	}
}

func TestLoadMergesInOrder(t *testing.T) {
	dir := configHome(t)
	write(t, dir, "config.json", `{
	  "theme": {"base": "dark", "colors": {"focused": "1", "dim": "2"}},
	  "keys":  {"leader": "ctrl+a", "bindings": {"traffic": {"J": "flow.next"}}}
	}`)
	write(t, dir, "work.json", `{
	  "theme": {"colors": {"focused": "9"}},
	  "keys":  {"bindings": {"traffic": {"K": "flow.prev"}}}
	}`)
	write(t, dir, "light.json", `{"theme": {"base": "light"}}`)

	got, err := Load([]string{"work", "light"}, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Last base wins.
	if got.Theme.Base != "light" {
		t.Errorf("base = %q, want light", got.Theme.Base)
	}
	// Later color overrides earlier...
	if got.Theme.Colors["focused"] != "9" {
		t.Errorf("focused = %q, want 9", got.Theme.Colors["focused"])
	}
	// ...but untouched keys survive from the earlier file.
	if got.Theme.Colors["dim"] != "2" {
		t.Errorf("dim = %q, want 2 (from the base file)", got.Theme.Colors["dim"])
	}
	// Bindings merge per context rather than replacing the context wholesale.
	binds := got.Keys.Bindings["traffic"]
	if binds["J"] != "flow.next" || binds["K"] != "flow.prev" {
		t.Errorf("bindings = %v, want both J and K", binds)
	}
	if got.Keys.Leader != "ctrl+a" {
		t.Errorf("leader = %q, want ctrl+a", got.Keys.Leader)
	}
	if len(got.Files) != 3 {
		t.Errorf("Files = %v, want 3 entries", got.Files)
	}
}

func TestLoadWithoutAnyConfig(t *testing.T) {
	configHome(t)
	got, err := Load(nil, "")
	if err != nil {
		t.Fatalf("Load with no files: %v", err)
	}
	if len(got.Files) != 0 {
		t.Errorf("Files = %v, want none", got.Files)
	}
	if !strings.Contains(got.Describe(), "built-in defaults") {
		t.Errorf("Describe() = %q", got.Describe())
	}
}

func TestMissingExplicitConfigIsAnError(t *testing.T) {
	configHome(t)
	_, err := Load([]string{"nope"}, "")
	if err == nil {
		t.Fatal("a -config file that doesn't exist must fail loudly")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error should name the spec, got %v", err)
	}
}

func TestLegacyConfigIsUsedOnlyWithoutTheNewOne(t *testing.T) {
	dir := configHome(t)
	dataDir := t.TempDir()
	write(t, dataDir, "config.json", `{"theme": {"base": "high-contrast"}}`)

	// No new-style config: the legacy file applies.
	got, err := Load(nil, dataDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Theme.Base != "high-contrast" {
		t.Errorf("legacy config ignored, base = %q", got.Theme.Base)
	}

	// Once the new-style file exists it wins outright, and the legacy file is
	// not merged underneath it.
	write(t, dir, "config.json", `{"theme": {"base": "light"}}`)
	got, err = Load(nil, dataDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Theme.Base != "light" {
		t.Errorf("base = %q, want light", got.Theme.Base)
	}
	for _, f := range got.Files {
		if strings.HasPrefix(f, dataDir) {
			t.Errorf("legacy file %q should not be read once the new one exists", f)
		}
	}
}

func TestUnknownKeysAreRejected(t *testing.T) {
	dir := configHome(t)
	write(t, dir, "config.json", `{"theme": {"base": "dark"}, "thmee": {}}`)
	if _, err := Load(nil, ""); err == nil {
		t.Fatal("a mistyped top-level section should be an error, not a no-op")
	}
}

func TestMalformedJSONNamesTheFile(t *testing.T) {
	dir := configHome(t)
	write(t, dir, "config.json", `{"theme": `)
	_, err := Load(nil, "")
	if err == nil {
		t.Fatal("truncated JSON should fail")
	}
	if !strings.Contains(err.Error(), "config.json") {
		t.Errorf("error should name the file, got %v", err)
	}
}
