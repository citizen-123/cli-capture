package theme

import "testing"

func TestResolveDefaultsToDark(t *testing.T) {
	got, err := Resolve("", nil)
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
	})
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
	}{
		{"unknown theme", "solarized", nil},
		{"unknown field", "dark", map[string]string{"focussed": "13"}},
		{"out of range", "dark", map[string]string{"focused": "256"}},
		{"negative", "dark", map[string]string{"focused": "-1"}},
		{"short hex", "dark", map[string]string{"focused": "#fff"}},
		{"not a color", "dark", map[string]string{"focused": "orange"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Resolve(c.base, c.colors); err == nil {
				t.Errorf("Resolve(%q, %v) succeeded, want an error", c.base, c.colors)
			}
		})
	}
}

func TestEmptyColorIsAllowed(t *testing.T) {
	// Explicitly blanking a field is how you say "render this one plain".
	got, err := Resolve("dark", map[string]string{"flag": ""})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Flag != "" {
		t.Errorf("flag = %q, want empty", got.Flag)
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
		if _, err := Resolve("dark", map[string]string{name: "1"}); err != nil {
			t.Errorf("advertised field %q is not settable: %v", name, err)
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
