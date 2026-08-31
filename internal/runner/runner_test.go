package runner

import (
	"reflect"
	"testing"
)

func TestLoginShell(t *testing.T) {
	tests := []struct {
		name  string
		shell string
		want  []string
	}{
		{name: "honors $SHELL", shell: "/bin/zsh", want: []string{"/bin/zsh"}},
		{name: "falls back when $SHELL is unset", shell: "", want: []string{"/bin/sh"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHELL", tc.shell)
			if got := LoginShell(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("LoginShell() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestShellCommand(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want []string
	}{
		{
			name: "plain command name stays bare so aliases still expand",
			argv: []string{"claude-as", "alt"},
			want: []string{"/bin/zsh", "-i", "-c", `claude-as 'alt'`},
		},
		{
			name: "command name with a space is quoted",
			argv: []string{"my tool", "x"},
			want: []string{"/bin/zsh", "-i", "-c", `'my tool' 'x'`},
		},
		{
			name: "command name with a metacharacter is quoted",
			argv: []string{"evil;rm -rf /"},
			want: []string{"/bin/zsh", "-i", "-c", `'evil;rm -rf /'`},
		},
		{
			name: "absolute path stays bare",
			argv: []string{"/usr/bin/curl", "https://example.com"},
			want: []string{"/bin/zsh", "-i", "-c", `/usr/bin/curl 'https://example.com'`},
		},
		{
			name: "arguments survive as literals",
			argv: []string{"echo", "$(whoami)", "a;b", "*", "one two"},
			want: []string{"/bin/zsh", "-i", "-c", `echo '$(whoami)' 'a;b' '*' 'one two'`},
		},
		{
			name: "embedded single quote is escaped",
			argv: []string{"echo", "it's"},
			want: []string{"/bin/zsh", "-i", "-c", `echo 'it'\''s'`},
		},
		{
			name: "no arguments yields a bare command word",
			argv: []string{"claude-as"},
			want: []string{"/bin/zsh", "-i", "-c", `claude-as`},
		},
		{
			name: "empty argv falls back to the login shell",
			argv: nil,
			want: []string{"/bin/zsh"},
		},
		// Boundary cases for the bare-argv[0] charset. These pin which command
		// names stay unquoted, so widening the charset has to fail a test
		// rather than quietly changing how the shell parses the first word.
		{
			name: "leading dash stays bare",
			argv: []string{"-x"},
			want: []string{"/bin/zsh", "-i", "-c", `-x`},
		},
		{
			name: "env-assignment prefix stays bare",
			argv: []string{"FOO=bar", "cmd"},
			want: []string{"/bin/zsh", "-i", "-c", `FOO=bar 'cmd'`},
		},
		{
			name: "leading dot stays bare",
			argv: []string{".", "script.sh"},
			want: []string{"/bin/zsh", "-i", "-c", `. 'script.sh'`},
		},
		{
			name: "leading percent stays bare",
			argv: []string{"%1"},
			want: []string{"/bin/zsh", "-i", "-c", `%1`},
		},
		{
			name: "tilde is outside the charset and gets quoted",
			argv: []string{"~/bin/tool"},
			want: []string{"/bin/zsh", "-i", "-c", `'~/bin/tool'`},
		},
		{
			name: "brace is outside the charset and gets quoted",
			argv: []string{"{a,b}"},
			want: []string{"/bin/zsh", "-i", "-c", `'{a,b}'`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHELL", "/bin/zsh")
			if got := ShellCommand(tc.argv); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ShellCommand(%q) = %q, want %q", tc.argv, got, tc.want)
			}
		})
	}
}
