package theme

import "testing"

func TestResolveDefaultsToDark(t *testing.T) {
	got, err := Resolve("", nil, nil, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	dark, _ := Builtin("dark")
	if got.Focused != dark.Focused || got.Status5xx != dark.Status5xx {
		t.Errorf("empty base should resolve to dark, got %+v", got)
	}
}

func TestResolveAppliesOverrides(t *testing.T) {
	got, err := Resolve("dark", map[string]string{
		"focused":     "#ff8800",
		"json.string": "42",
	}, nil, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Focused != "#ff8800" {
		t.Errorf("focused = %q, want #ff8800", got.Focused)
	}
	if got.JSONString != "42" {
		t.Errorf("json.string = %q, want 42", got.JSONString)
	}
	// Untouched fields keep the base value.
	dark, _ := Builtin("dark")
	if got.Status2xx != dark.Status2xx {
		t.Errorf("status2xx = %q, want the base %q", got.Status2xx, dark.Status2xx)
	}
}

func TestResolveRejectsBadInput(t *testing.T) {
	cases := []struct {
		name   string
		base   string
		colors map[string]string
		glyphs map[string]string
		border string
	}{
		{name: "unknown theme", base: "solarized"},
		{name: "unknown field", base: "dark", colors: map[string]string{"focussed": "13"}},
		{name: "out of range", base: "dark", colors: map[string]string{"focused": "256"}},
		{name: "negative", base: "dark", colors: map[string]string{"focused": "-1"}},
		{name: "short hex", base: "dark", colors: map[string]string{"focused": "#fff"}},
		{name: "not a color", base: "dark", colors: map[string]string{"focused": "orange"}},
		{name: "unknown glyph", base: "dark", glyphs: map[string]string{"pointerr": ">"}},
		{name: "unknown border", base: "dark", border: "fancy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Resolve(c.base, c.colors, c.glyphs, c.border); err == nil {
				t.Errorf("Resolve(%q, ...) succeeded, want an error", c.base)
			}
		})
	}
}

func TestEmptyColorIsAllowed(t *testing.T) {
	// Explicitly blanking a field is how you say "render this one plain".
	got, err := Resolve("dark", map[string]string{"flag": ""}, nil, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Flag != "" {
		t.Errorf("flag = %q, want empty", got.Flag)
	}
}

func TestResolveAppliesGlyphsAndBorder(t *testing.T) {
	got, err := Resolve("dark", nil, map[string]string{
		"flag":    "*",
		"pointer": ">",
		"arrow":   "-",
	}, "double")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.FlagGlyph != "*" || got.PointerGlyph != ">" || got.ArrowGlyph != "-" {
		t.Errorf("glyphs not applied: %+v", got)
	}
	if got.Border != "double" {
		t.Errorf("border = %q, want double", got.Border)
	}
}

func TestColorlessKeepsGlyphsAndBorder(t *testing.T) {
	// NO_COLOR strips colors but must not undo a user's chosen glyphs/border.
	th, err := Resolve("dark", nil, map[string]string{"flag": "*"}, "thick")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := th.Colorless()
	if got.Focused != "" {
		t.Errorf("Colorless left a color behind: %q", got.Focused)
	}
	if got.FlagGlyph != "*" || got.Border != "thick" {
		t.Errorf("Colorless dropped glyph/border: %+v", got)
	}
}

func TestColorlessStripsEverything(t *testing.T) {
	dark, _ := Builtin("dark")
	got := dark.Colorless()
	if got.Focused != "" || got.Title != "" || got.JSONKey != "" || got.StatusMode != "" {
		t.Errorf("Colorless left color behind: %+v", got)
	}
}

func TestEveryFieldIsSettable(t *testing.T) {
	// Fields() is what error messages and docs advertise, so every name in it
	// must actually resolve to a field.
	for _, name := range Fields() {
		if _, err := Resolve("dark", map[string]string{name: "1"}, nil, ""); err != nil {
			t.Errorf("advertised field %q is not settable: %v", name, err)
		}
	}
}

func TestEveryGlyphIsSettable(t *testing.T) {
	// GlyphFields() is what errors and docs advertise; each must resolve.
	for _, name := range GlyphFields() {
		if _, err := Resolve("dark", nil, map[string]string{name: "x"}, ""); err != nil {
			t.Errorf("advertised glyph %q is not settable: %v", name, err)
		}
	}
}

func TestEveryBorderIsAccepted(t *testing.T) {
	for _, name := range Borders() {
		if _, err := Resolve("dark", nil, nil, name); err != nil {
			t.Errorf("advertised border %q is rejected: %v", name, err)
		}
	}
}

func TestBuiltinsAreComplete(t *testing.T) {
	// Every built-in except "none" should define every color; a missing one
	// renders plain and looks like a bug rather than a choice.
	for _, name := range Names() {
		if name == "none" {
			continue
		}
		th, _ := Builtin(name)
		for _, f := range Fields() {
			if v := th.field(f); v == nil || *v == "" {
				t.Errorf("built-in %q leaves %q unset", name, f)
			}
		}
	}
}

func TestBuiltinReturnsACopy(t *testing.T) {
	a, _ := Builtin("dark")
	a.Focused = "mutated"
	b, _ := Builtin("dark")
	if b.Focused == "mutated" {
		t.Error("Builtin handed out a reference to the shared theme")
	}
}
