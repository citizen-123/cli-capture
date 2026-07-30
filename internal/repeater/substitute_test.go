package repeater

import (
	"reflect"
	"testing"
)

func TestVariables(t *testing.T) {
	got := Variables("GET /{{path}}?u={{ user }}&u2={{user}} with {{token}}")
	want := []string{"path", "user", "token"} // distinct, first-seen order
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Variables = %v, want %v", got, want)
	}
	if v := Variables("no markers here"); len(v) != 0 {
		t.Errorf("expected no variables, got %v", v)
	}
}

func TestSubstitute(t *testing.T) {
	in := "Authorization: Bearer {{token}}; user={{user}}; keep={{unknown}}"
	got := Substitute(in, map[string]string{"token": "abc", "user": "ada"})
	want := "Authorization: Bearer abc; user=ada; keep={{unknown}}"
	if got != want {
		t.Errorf("Substitute = %q, want %q", got, want)
	}
}

func TestSubstituteWhitespaceTolerant(t *testing.T) {
	if got := Substitute("{{ x }}", map[string]string{"x": "1"}); got != "1" {
		t.Errorf("whitespace-padded marker not substituted: %q", got)
	}
}
