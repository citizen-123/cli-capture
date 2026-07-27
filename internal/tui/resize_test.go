package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
)

// The two pane widths must always leave room for their borders and never
// overflow the terminal: leftPaneWidth + rightPaneWidth == m.width - 2 (the
// four border columns take the remaining two), for every ratio.
func TestPaneWidthsFitAndRespectRatio(t *testing.T) {
	const w = 80
	for _, r := range []float64{minSplit, 0.35, 0.5, 0.65, maxSplit} {
		m := Model{width: w, splitRatio: r}
		l, right := m.leftPaneWidth(), m.rightPaneWidth()
		if l+right != w-2 {
			t.Errorf("ratio %v: left(%d)+right(%d) = %d, want %d", r, l, right, l+right, w-2)
		}
		if l < 1 || right < 1 {
			t.Errorf("ratio %v: non-positive pane width left=%d right=%d", r, l, right)
		}
	}

	// A 50/50 split must reproduce the old hardcoded width/2 - 1 exactly.
	if got := (Model{width: w, splitRatio: 0.5}).leftPaneWidth(); got != w/2-1 {
		t.Errorf("leftPaneWidth at 0.5 = %d, want %d", got, w/2-1)
	}

	// The left pane grows as the ratio grows.
	lo := (Model{width: w, splitRatio: 0.3}).leftPaneWidth()
	hi := (Model{width: w, splitRatio: 0.7}).leftPaneWidth()
	if hi <= lo {
		t.Errorf("left pane should grow with ratio: lo=%d hi=%d", lo, hi)
	}
}

// adjustSplit clamps the ratio to [minSplit, maxSplit] no matter how far it is
// pushed. target and screen are nil here, which resizeChild tolerates.
func TestAdjustSplitClamps(t *testing.T) {
	m := Model{width: 80, splitRatio: 0.5, ta: newEditor(), vp: viewport.New(0, 0)}
	for i := 0; i < 100; i++ {
		m.adjustSplit(splitStep)
	}
	if m.splitRatio > maxSplit {
		t.Errorf("ratio exceeded max: %v > %v", m.splitRatio, maxSplit)
	}
	for i := 0; i < 100; i++ {
		m.adjustSplit(-splitStep)
	}
	if m.splitRatio < minSplit {
		t.Errorf("ratio dropped below min: %v < %v", m.splitRatio, minSplit)
	}
}
