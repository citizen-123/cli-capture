package terminal

import "testing"

// TestVTMouseModeTracking pins the DECSET sequences the TUI relies on to know
// whether it's safe to forward mouse events: a target must explicitly opt in
// (mode 1000/1002/1003) before cli-capture writes anything mouse-shaped into
// its stdin, and SGR (1006) is tracked separately since legacy X10 encoding
// isn't forwarded at all (see internal/tui/model.go).
func TestVTMouseModeTracking(t *testing.T) {
	tests := []struct {
		name      string
		seq       string
		wantMouse bool
		wantSGR   bool
	}{
		{"nothing enabled", "", false, false},
		{"button tracking only", "\x1b[?1000h", true, false},
		{"motion tracking only", "\x1b[?1002h", true, false},
		{"any-motion tracking only", "\x1b[?1003h", true, false},
		{"SGR without a tracking mode", "\x1b[?1006h", false, true},
		{"button + SGR", "\x1b[?1000h\x1b[?1006h", true, true},
		{"enabled then disabled", "\x1b[?1000h\x1b[?1000l", false, false},
		{"SGR enabled then disabled", "\x1b[?1000h\x1b[?1006h\x1b[?1006l", true, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vt := NewVT(20, 5, nil)
			if tc.seq != "" {
				if _, err := vt.Write([]byte(tc.seq)); err != nil {
					t.Fatalf("Write(%q): %v", tc.seq, err)
				}
			}
			if got := vt.MouseEnabled(); got != tc.wantMouse {
				t.Errorf("MouseEnabled() = %v, want %v", got, tc.wantMouse)
			}
			if got := vt.MouseSGR(); got != tc.wantSGR {
				t.Errorf("MouseSGR() = %v, want %v", got, tc.wantSGR)
			}
		})
	}
}

// TestScreenNeverClaimsMouseSupport pins the line-oriented Screen's Emulator
// contract: it has no mode state, so it must never tell the TUI a mouse event
// is safe to forward.
func TestScreenNeverClaimsMouseSupport(t *testing.T) {
	s := New()
	if s.MouseEnabled() {
		t.Error("Screen.MouseEnabled() should always be false")
	}
	if s.MouseSGR() {
		t.Error("Screen.MouseSGR() should always be false")
	}
}
