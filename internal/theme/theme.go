// Package theme holds the color palette the TUI renders with. It is plain data
// — no lipgloss, no terminal — so themes can be loaded, merged, and validated
// away from the rendering code, and so the config layer doesn't have to know
// what a style is.
//
// A theme is resolved in three steps: start from a built-in base, apply the
// user's color overrides, then drop all color if the terminal (or NO_COLOR)
// says so. Every step is total: an unset field just keeps the base's value.
package theme

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
)

// Theme is the full palette. Each field is a terminal color: an ANSI 256 index
// ("13"), a hex triplet ("#ff8800"), or empty for "no color, render plain".
type Theme struct {
	Name string

	// Pane borders.
	Focused   string
	Unfocused string

	// General chrome.
	Title   string
	Pending string
	Flag    string
	KeyCap  string
	Dim     string
	Section string

	// HTTP status-class colors for the flow-list status column.
	Status2xx string
	Status3xx string
	Status4xx string
	Status5xx string

	// JSON syntax highlighting for pretty-printed bodies.
	JSONKey     string
	JSONString  string
	JSONNumber  string
	JSONLiteral string
	JSONPunct   string

	// Status bar: the mode chip and the message text beside it.
	StatusMode string
	StatusText string

	// Glyphs are the short strings the UI draws for the flagged-row marker, the
	// paused/edit pointer, and the detail/repeater title arrow. Terminal font
	// coverage varies, so these are themeable alongside the colors. Empty means
	// "use the built-in default" — the rendering layer supplies it, the same way
	// an empty color means "no color". Kept apart from the palette so NO_COLOR
	// strips colors without touching glyphs.
	FlagGlyph    string
	PointerGlyph string
	ArrowGlyph   string

	// Border is the pane/overlay border style, one of Borders(). Empty means the
	// default (rounded).
	Border string
}

// field returns a pointer to the named color field, or nil if the name is not
// a known field. The names are the keys users write in a config file.
func (t *Theme) field(name string) *string {
	switch name {
	case "focused":
		return &t.Focused
	case "unfocused":
		return &t.Unfocused
	case "title":
		return &t.Title
	case "pending":
		return &t.Pending
	case "flag":
		return &t.Flag
	case "keycap":
		return &t.KeyCap
	case "dim":
		return &t.Dim
	case "section":
		return &t.Section
	case "status2xx":
		return &t.Status2xx
	case "status3xx":
		return &t.Status3xx
	case "status4xx":
		return &t.Status4xx
	case "status5xx":
		return &t.Status5xx
	case "json.key":
		return &t.JSONKey
	case "json.string":
		return &t.JSONString
	case "json.number":
		return &t.JSONNumber
	case "json.literal":
		return &t.JSONLiteral
	case "json.punct":
		return &t.JSONPunct
	case "statusbar.mode":
		return &t.StatusMode
	case "statusbar.text":
		return &t.StatusText
	}
	return nil
}

// Fields lists every settable color name, sorted, for error messages and docs.
func Fields() []string {
	names := []string{
		"focused", "unfocused", "title", "pending", "flag", "keycap", "dim",
		"section", "status2xx", "status3xx", "status4xx", "status5xx",
		"json.key", "json.string", "json.number", "json.literal", "json.punct",
		"statusbar.mode", "statusbar.text",
	}
	sort.Strings(names)
	return names
}

// glyphField returns a pointer to the named glyph field, or nil if unknown.
// These are the keys users write in the `glyphs` config section.
func (t *Theme) glyphField(name string) *string {
	switch name {
	case "flag":
		return &t.FlagGlyph
	case "pointer":
		return &t.PointerGlyph
	case "arrow":
		return &t.ArrowGlyph
	}
	return nil
}

// GlyphFields lists every settable glyph name, sorted, for errors and docs.
func GlyphFields() []string { return []string{"arrow", "flag", "pointer"} }

// borders is the set of accepted pane border styles. Empty means the default.
var borders = map[string]bool{
	"": true, "rounded": true, "normal": true, "thick": true,
	"double": true, "hidden": true,
}

// Borders lists the selectable border styles, sorted, for errors and docs.
func Borders() []string { return []string{"double", "hidden", "normal", "rounded", "thick"} }

var builtins = map[string]Theme{
	// The original palette: 256-color indices chosen for a dark terminal.
	"dark": {
		Name: "dark", Focused: "13", Unfocused: "240",
		Title: "14", Pending: "11", Flag: "208", KeyCap: "14", Dim: "245", Section: "12",
		Status2xx: "10", Status3xx: "14", Status4xx: "11", Status5xx: "9",
		JSONKey: "12", JSONString: "10", JSONNumber: "11", JSONLiteral: "13", JSONPunct: "240",
		StatusMode: "10", StatusText: "245",
	},
	// Darker foregrounds so nothing washes out on a light background.
	"light": {
		Name: "light", Focused: "5", Unfocused: "250",
		Title: "6", Pending: "3", Flag: "166", KeyCap: "6", Dim: "243", Section: "4",
		Status2xx: "2", Status3xx: "6", Status4xx: "3", Status5xx: "1",
		JSONKey: "4", JSONString: "2", JSONNumber: "3", JSONLiteral: "5", JSONPunct: "243",
		StatusMode: "2", StatusText: "243",
	},
	// The 16 basic colors only, at maximum separation — for low-color
	// terminals and for anyone who finds the 256-color palette muddy.
	"high-contrast": {
		Name: "high-contrast", Focused: "15", Unfocused: "8",
		Title: "15", Pending: "11", Flag: "9", KeyCap: "15", Dim: "7", Section: "14",
		Status2xx: "10", Status3xx: "14", Status4xx: "11", Status5xx: "9",
		JSONKey: "14", JSONString: "10", JSONNumber: "11", JSONLiteral: "13", JSONPunct: "7",
		StatusMode: "10", StatusText: "15",
	},
	// Every field empty: styling still applies (bold, reverse, borders) but no
	// color is emitted. This is what NO_COLOR resolves to.
	"none": {Name: "none"},
}

// Default is the theme used when nothing is configured.
const Default = "dark"

// Names lists the built-in theme names, sorted.
func Names() []string {
	out := make([]string, 0, len(builtins))
	for name := range builtins {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Builtin returns a copy of a built-in theme.
func Builtin(name string) (Theme, bool) {
	t, ok := builtins[name]
	return t, ok
}

var hexRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// validColor accepts an ANSI 256 index, a #rrggbb triplet, or empty (meaning
// "no color"). Rejecting anything else turns a typo into an error at startup
// instead of a silently unstyled row.
func validColor(v string) bool {
	if v == "" || hexRe.MatchString(v) {
		return true
	}
	n, err := strconv.Atoi(v)
	return err == nil && n >= 0 && n <= 255
}

// Resolve builds a theme from a built-in base plus per-field overrides. An
// empty base means the default theme. Unknown field names and malformed colors
// are errors — a config file that doesn't do what it says is worse than one
// that refuses to load.
func Resolve(base string, colors, glyphs map[string]string, border string) (Theme, error) {
	if base == "" {
		base = Default
	}
	t, ok := Builtin(base)
	if !ok {
		return Theme{}, fmt.Errorf("theme: unknown theme %q (have %v)", base, Names())
	}
	// Sorted so a file with several bad keys always reports the same one first.
	for _, name := range sortedKeys(colors) {
		p := t.field(name)
		if p == nil {
			return Theme{}, fmt.Errorf("theme: unknown color %q (have %v)", name, Fields())
		}
		v := colors[name]
		if !validColor(v) {
			return Theme{}, fmt.Errorf("theme: color %q: invalid value %q (want 0-255 or #rrggbb)", name, v)
		}
		*p = v
	}
	for _, name := range sortedKeys(glyphs) {
		p := t.glyphField(name)
		if p == nil {
			return Theme{}, fmt.Errorf("theme: unknown glyph %q (have %v)", name, GlyphFields())
		}
		*p = glyphs[name]
	}
	if border != "" {
		if !borders[border] {
			return Theme{}, fmt.Errorf("theme: unknown border %q (have %v)", border, Borders())
		}
		t.Border = border
	}
	if base != Default || len(colors) > 0 || len(glyphs) > 0 || border != "" {
		t.Name = base
	}
	return t, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Colorless returns the theme with every color stripped, keeping its name (so
// the reason is still visible in logs) and its glyphs and border (which aren't
// colors — NO_COLOR shouldn't silently swap a user's chosen marker back).
func (t Theme) Colorless() Theme {
	return Theme{
		Name:         t.Name,
		FlagGlyph:    t.FlagGlyph,
		PointerGlyph: t.PointerGlyph,
		ArrowGlyph:   t.ArrowGlyph,
		Border:       t.Border,
	}
}
