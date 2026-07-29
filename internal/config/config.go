// Package config loads cli-capture's user configuration: the theme and the
// keymap, as JSON, from one or more files.
//
// Resolution order, each layer overriding the one before it:
//
//	built-in defaults
//	~/.config/cli-capture/config.json      (or $XDG_CONFIG_HOME/cli-capture)
//	each -config file, in the order given
//
// A -config value that looks like a bare name rather than a path resolves to a
// preset in the config directory: `-config work` loads
// ~/.config/cli-capture/work.json. Several may be given — `-config base,dark`
// or a repeated flag — and they merge left to right, so a preset can carry one
// override without restating everything before it.
//
// Missing files are only tolerated for the default: a -config file that isn't
// there is an error, because the user asked for it by name.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Config is the whole user configuration. Sections are merged field by field,
// so a file may set only what it changes.
type Config struct {
	Theme ThemeSection `json:"theme"`
	Keys  KeysSection  `json:"keys"`
}

// ThemeSection selects a built-in palette and overrides individual colors,
// glyphs, and the border style on top of it. Names and values are validated by
// the theme package, not here.
type ThemeSection struct {
	Base   string            `json:"base"`
	Colors map[string]string `json:"colors"`
	Glyphs map[string]string `json:"glyphs"`
	Border string            `json:"border"`
}

// KeysSection sets the leader key and rebinds keys per context. Action names
// and contexts are validated by the TUI, which owns them.
type KeysSection struct {
	Leader   string                       `json:"leader"`
	Bindings map[string]map[string]string `json:"bindings"`
}

// Loaded is a merged configuration plus the files that produced it, so the
// startup log can say where a setting actually came from.
type Loaded struct {
	Config
	Files []string
}

// Dir is the directory holding config.json and any presets:
// $XDG_CONFIG_HOME/cli-capture, falling back to ~/.config/cli-capture.
func Dir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "cli-capture")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "cli-capture")
	}
	return filepath.Join(home, ".config", "cli-capture")
}

// DefaultPath is the config file loaded when no -config is given (and as the
// base layer when one is).
func DefaultPath() string { return filepath.Join(Dir(), "config.json") }

// LegacyPath is where config lived when it shared the data directory. It is
// read only when DefaultPath doesn't exist, so an existing install keeps
// working without being silently overridden by an empty new-style file.
func LegacyPath(dataDir string) string { return filepath.Join(dataDir, "config.json") }

// Resolve turns a -config value into a path. Anything containing a path
// separator, starting with "." or "~", or ending in .json is taken as a path;
// everything else is a preset name in Dir().
func Resolve(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ""
	}
	if strings.HasPrefix(spec, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, spec[2:])
		}
	}
	if strings.ContainsRune(spec, filepath.Separator) || strings.ContainsRune(spec, '/') ||
		strings.HasPrefix(spec, ".") || strings.HasSuffix(spec, ".json") {
		return spec
	}
	return filepath.Join(Dir(), spec+".json")
}

// Split expands a comma-separated flag value into individual specs.
func Split(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Paths collects -config values. It implements flag.Value so the flag can be
// repeated *and* comma-separated, rather than the last one silently winning.
type Paths []string

func (p *Paths) String() string { return strings.Join(*p, ",") }

func (p *Paths) Set(v string) error {
	*p = append(*p, Split(v)...)
	return nil
}

// ReadFile parses one config file. Unknown keys are rejected: a mistyped
// section that silently does nothing is the worst kind of config bug.
func ReadFile(path string) (Config, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("config: %s: %w", path, err)
	}
	return c, nil
}

// Merge layers other on top of c: non-empty scalars replace, maps merge per
// key so an override file can change one color or one binding.
func (c *Config) Merge(other Config) {
	if other.Theme.Base != "" {
		c.Theme.Base = other.Theme.Base
	}
	for name, v := range other.Theme.Colors {
		if c.Theme.Colors == nil {
			c.Theme.Colors = map[string]string{}
		}
		c.Theme.Colors[name] = v
	}
	for name, v := range other.Theme.Glyphs {
		if c.Theme.Glyphs == nil {
			c.Theme.Glyphs = map[string]string{}
		}
		c.Theme.Glyphs[name] = v
	}
	if other.Theme.Border != "" {
		c.Theme.Border = other.Theme.Border
	}
	if other.Keys.Leader != "" {
		c.Keys.Leader = other.Keys.Leader
	}
	for ctx, binds := range other.Keys.Bindings {
		if c.Keys.Bindings == nil {
			c.Keys.Bindings = map[string]map[string]string{}
		}
		if c.Keys.Bindings[ctx] == nil {
			c.Keys.Bindings[ctx] = map[string]string{}
		}
		for key, action := range binds {
			c.Keys.Bindings[ctx][key] = action
		}
	}
}

// Load reads the default config (or the legacy one) and then each spec in
// order, merging as it goes. dataDir is the -dir value, used only to find a
// legacy config; pass "" to skip that lookup.
func Load(specs []string, dataDir string) (*Loaded, error) {
	out := &Loaded{}

	base := DefaultPath()
	switch cfg, err := ReadFile(base); {
	case err == nil:
		out.Merge(cfg)
		out.Files = append(out.Files, base)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	case dataDir != "":
		// No new-style config; fall back to the old location if it's there.
		legacy := LegacyPath(dataDir)
		cfg, err := ReadFile(legacy)
		if err == nil {
			out.Merge(cfg)
			out.Files = append(out.Files, legacy)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}

	for _, spec := range specs {
		path := Resolve(spec)
		cfg, err := ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("config: %s: no such file (from -config %s)", path, spec)
			}
			return nil, err
		}
		out.Merge(cfg)
		out.Files = append(out.Files, path)
	}
	return out, nil
}

// Describe summarizes what was loaded, for the startup log.
func (l *Loaded) Describe() string {
	if len(l.Files) == 0 {
		return "built-in defaults (no config file)"
	}
	return strings.Join(l.Files, " + ")
}
