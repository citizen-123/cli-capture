package shellquote

import "testing"

func TestSingle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain word", in: "alt", want: `'alt'`},
		{name: "empty string", in: "", want: `''`},
		{name: "spaces stay one word", in: "one two", want: `'one two'`},
		{name: "metacharacters stay literal", in: "$(whoami);rm *", want: `'$(whoami);rm *'`},
		{name: "embedded quote is escaped", in: "it's", want: `'it'\''s'`},
		{name: "only a quote", in: "'", want: `''\'''`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Single(tc.in); got != tc.want {
				t.Errorf("Single(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
