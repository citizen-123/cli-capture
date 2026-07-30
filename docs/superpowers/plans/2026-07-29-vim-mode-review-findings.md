# Vim Mode Review Findings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Correct every issue found in the deep review of the issue #9 Vim-style traffic-pane spike, while preserving terminal-pane transparency and configuration precedence.

**Architecture:** Keep command and key behavior behind shared `Model` operations instead of parallel state mutations. Render Vim help from the effective keymap, define one user-facing host identity for host motions, and verify terminal isolation through an actual pipe-backed `runner.Target`.

**Tech Stack:** Go, Bubble Tea, Bubbles `textinput`, Lip Gloss, standard `testing`, standard `os`/`io`/`net` packages.

## Global Constraints

- Follow test-driven development: add one focused failing test, observe the expected failure, then make the smallest production change.
- Vim prefixes, motions, and the `:` command line remain traffic-pane-only.
- Explicit keymap bindings, including `none`, always take precedence over built-in Vim fallback handling.
- In-app help must describe effective runtime behavior, not defaults that configuration has disabled.
- Do not add clipboard support or a new dependency; `:curl` remains a file export and its wording must say so.
- Keep the existing no-wrap and count semantics for host motions.
- Run `gofmt` on every changed Go file.

---

## File Structure

- `internal/tui/model.go`: shared selected-flow flag operation and traffic-key dispatch.
- `internal/tui/command.go`: command metadata/dispatch and the shared host identity used by host motions.
- `internal/tui/help.go`: configuration-aware Vim motion rows and command names with aliases.
- `internal/tui/command_test.go`: command/key parity, motion availability, host grouping, and terminal forwarding behavior.
- `internal/tui/help_test.go`: effective-keymap help and alias discoverability.
- `docs/keybindings.md`: accurate command, motion, alias, and host-jump behavior.
- `docs/configuration.md`: precise per-key effects of overriding `g`, `G`, or digits.
- `README.md`: only update if its curl wording implies copying rather than exporting.

---

### Task 1: Make Space and `:flag` Share One Safe Operation

**Files:**
- Modify: `internal/tui/model.go:178-184,542-545`
- Modify: `internal/tui/command.go:98-115`
- Test: `internal/tui/command_test.go`

**Interfaces:**
- Produces: `func (m *Model) toggleSelectedFlag() string`
- Consumes: existing `selectedFlow()`, `visible()`, `clampIndex()`, and `capture.Flow.Title()`

- [ ] **Step 1: Write a failing parity regression test**

Add a test that starts from two flagged flows in flagged-only mode, selects the last row, invokes Space and `:flag` from identical model states, and asserts both models retain a valid selection:

```go
func TestFlagCommandMatchesSpaceInFlaggedOnlyView(t *testing.T) {
	base := modelWithFlows(2)
	for _, f := range base.flows {
		f.Flagged = true
	}
	base.flaggedOnly = true
	base.selected = 1

	viaKey := base
	viaKey.flows = append([]*capture.Flow(nil), base.flows...)
	keyFlow := *base.flows[1]
	viaKey.flows[1] = &keyFlow

	viaCommand := base
	viaCommand.flows = append([]*capture.Flow(nil), base.flows...)
	commandFlow := *base.flows[1]
	viaCommand.flows[1] = &commandFlow

	next, _ := viaKey.onKey(key(" "))
	viaKey = next.(Model)
	next, _ = viaCommand.runCommand("flag")
	viaCommand = next.(Model)

	if viaKey.selected != 0 || viaKey.selectedFlow() == nil {
		t.Fatalf("Space left selected=%d with selectedFlow=%v; want row 0 selected",
			viaKey.selected, viaKey.selectedFlow())
	}
	if viaCommand.selected != viaKey.selected {
		t.Errorf(":flag selected=%d, Space selected=%d", viaCommand.selected, viaKey.selected)
	}
	if viaCommand.status != viaKey.status {
		t.Errorf(":flag status=%q, Space status=%q", viaCommand.status, viaKey.status)
	}
}
```

Ensure the two model copies do not share the flow being mutated. If a smaller clone helper makes the test clearer, keep that helper in `command_test.go`.

- [ ] **Step 2: Run the test and verify the stale-selection failure**

Run:

```bash
go test ./internal/tui -run TestFlagCommandMatchesSpaceInFlaggedOnlyView -count=1
```

Expected: FAIL because Space leaves `selected == 1` after the visible list shrinks to one row, while `:flag` clamps to row zero.

- [ ] **Step 3: Add the shared flag operation**

Add beside `selectedFlow()`:

```go
func (m *Model) toggleSelectedFlag() string {
	f := m.selectedFlow()
	if f == nil {
		return "nothing selected to flag"
	}
	f.Flagged = !f.Flagged
	m.selected = clampIndex(m.selected, len(m.visible()))
	if f.Flagged {
		return "flagged " + f.Title()
	}
	return "unflagged " + f.Title()
}
```

Replace the `ActFlowFlag` body with:

```go
case ActFlowFlag:
	m.status = m.toggleSelectedFlag()
```

Replace the `:flag` handler with:

```go
run: func(m Model, _ string) (Model, tea.Cmd) {
	m.status = m.toggleSelectedFlag()
	return m, nil
},
```

- [ ] **Step 4: Run focused and package tests**

Run:

```bash
gofmt -w internal/tui/model.go internal/tui/command.go internal/tui/command_test.go
go test ./internal/tui -run 'TestFlag(CommandMatchesSpaceInFlaggedOnlyView|gedCommandTogglesTheViewRatherThanWriting)' -count=1
go test ./internal/tui
```

Expected: all pass.

- [ ] **Step 5: Commit the behavior fix**

```bash
git add internal/tui/model.go internal/tui/command.go internal/tui/command_test.go
git commit -m "tui: keep flag selection and command behavior aligned"
```

---

### Task 2: Make Vim Help Reflect Configuration and Expose Command Aliases

**Files:**
- Modify: `internal/tui/help.go:55-77`
- Modify: `docs/configuration.md:188-202`
- Modify: `docs/keybindings.md:55-73,95-106`
- Test: `internal/tui/help_test.go`

**Interfaces:**
- Produces: `func motionHelpRows(km KeyMap) [][2]string`
- Produces: `func commandHelpName(c command) string`
- Consumes: `KeyMap.claims(ctx, key)`, `command.name`, `command.aliases`, and `command.args`

- [ ] **Step 1: Write failing tests for effective motion help**

Add a table-driven test:

```go
func TestVimHelpFollowsClaimedKeys(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]map[string]string
		present   []string
		absent    []string
	}{
		{
			name: "G disabled leaves gg available",
			overrides: map[string]map[string]string{
				ctxTraffic: {"G": string(Unbind)},
			},
			present: []string{"gg"},
			absent:  []string{"gg / G", "{count}G"},
		},
		{
			name: "g rebound leaves G available",
			overrides: map[string]map[string]string{
				ctxTraffic: {"g": string(ActFlowPrev)},
			},
			present: []string{"G"},
			absent:  []string{"gg / G"},
		},
		{
			name: "digit claimed hides counted motions",
			overrides: map[string]map[string]string{
				ctxTraffic: {"5": string(ActFlowNext)},
			},
			present: []string{"gg / G"},
			absent:  []string{"{count}", "{count}G"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			km, err := NewKeyMap("", tc.overrides)
			if err != nil {
				t.Fatal(err)
			}
			help := Model{}.WithKeys(km).helpBody()
			_, motionSection, _ := strings.Cut(help, vimHelpHeading)
			motionSection, _, _ = strings.Cut(motionSection, "Traffic pane — : commands")
			for _, want := range tc.present {
				if !strings.Contains(motionSection, want) {
					t.Errorf("motion help missing %q", want)
				}
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(motionSection, unwanted) {
					t.Errorf("motion help unexpectedly contains %q", unwanted)
				}
			}
		})
	}
}
```

The exact rendered labels may be split into separate `gg` and `G` rows; keep assertions tied to user-visible behavior rather than ANSI styling.

- [ ] **Step 2: Write a failing alias-discoverability test**

```go
func TestCommandHelpIncludesAliases(t *testing.T) {
	out := (Model{}).helpBody()
	for _, want := range []string{
		":filter (:f)",
		":curl (:y)",
		":resend (:x)",
		":flagged (:only)",
		":w (:write, :save)",
		":q (:quit)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("command help missing canonical/alias display %q", want)
		}
	}
}
```

- [ ] **Step 3: Run both tests and verify the expected failures**

Run:

```bash
go test ./internal/tui -run 'Test(VimHelpFollowsClaimedKeys|CommandHelpIncludesAliases)' -count=1
```

Expected: FAIL because motion rows are unconditional and aliases are omitted.

- [ ] **Step 4: Render only available motion capabilities**

Add a helper that:

- considers `gg` available only when `g` is not claimed;
- considers `G` available only when `G` is not claimed;
- considers counts available only when none of `"0"` through `"9"` is claimed;
- renders `gg`, `G`, counted repetition, and counted goto as separate rows so partial availability is accurately expressible;
- always renders `esc` as the pending-motion cancellation key.

Use this signature and shape:

```go
func motionHelpRows(km KeyMap) [][2]string {
	digitsFree := true
	for key := '0'; key <= '9'; key++ {
		if km.claims(ctxTraffic, string(key)) {
			digitsFree = false
			break
		}
	}

	var rows [][2]string
	if digitsFree {
		rows = append(rows, [2]string{"{count}", "repeat the next available motion: 5j, 3k, 2}"})
	}
	if !km.claims(ctxTraffic, "g") {
		rows = append(rows, [2]string{"gg", "first flow"})
		if digitsFree {
			rows = append(rows, [2]string{"{count}gg", "jump to that row — 5gg goes to row 5"})
		}
	}
	if !km.claims(ctxTraffic, "G") {
		rows = append(rows, [2]string{"G", "last flow"})
		if digitsFree {
			rows = append(rows, [2]string{"{count}G", "jump to that row — 12G goes to row 12"})
		}
	}
	rows = append(rows, [2]string{"esc", "discard a half-typed motion"})
	return rows
}
```

Replace the four unconditional calls with a loop over `motionHelpRows(km)`.

- [ ] **Step 5: Render aliases from command metadata**

Add:

```go
func commandHelpName(c command) string {
	name := ":" + c.name
	if len(c.aliases) > 0 {
		aliases := make([]string, len(c.aliases))
		for i, alias := range c.aliases {
			aliases[i] = ":" + alias
		}
		name += " (" + strings.Join(aliases, ", ") + ")"
	}
	if c.args != "" {
		name += " " + c.args
	}
	return name
}
```

Use it when building the command help names and calculating `cmdWidth`.

- [ ] **Step 6: Correct configuration and keybinding documentation**

In `docs/configuration.md`, replace the blanket warning with exact effects:

```markdown
The built-in motion layer only sees keys the traffic keymap has not claimed.
Binding or disabling `g` removes `gg`; binding or disabling `G` removes `G`
and `{count}G`. Claiming any digit disables counted-motion help because that
prefix is no longer fully available. The live help overlay reflects these
changes.
```

In `docs/keybindings.md`, state that the motion rows shown by `?` follow these same availability rules. Keep the alias table, and say that aliases also appear beside canonical command names in the in-app overlay.

- [ ] **Step 7: Run focused and package tests**

```bash
gofmt -w internal/tui/help.go internal/tui/help_test.go
go test ./internal/tui -run 'Test(VimHelpFollowsClaimedKeys|CommandHelpIncludesAliases|Help)' -count=1
go test ./internal/tui
```

Expected: all pass.

- [ ] **Step 8: Commit the help/configuration fix**

```bash
git add internal/tui/help.go internal/tui/help_test.go docs/configuration.md docs/keybindings.md
git commit -m "tui: make vim help match configured behavior"
```

---

### Task 3: Describe `:curl` as a File Export

**Files:**
- Modify: `internal/tui/command.go:94`
- Modify: `docs/keybindings.md:81-93`
- Inspect and modify if needed: `README.md:245-315`
- Inspect: `docs/exporting.md:1-20`
- Test: `internal/tui/help_test.go`

**Interfaces:**
- No new production interface.
- Preserves: `func (m Model) exportCurlSelected() string`

- [ ] **Step 1: Write a failing user-facing copy test**

Add:

```go
func TestCurlCommandHelpDescribesAFileExport(t *testing.T) {
	out := (Model{}).helpBody()
	if strings.Contains(out, "copy the selected flow") {
		t.Error(":curl help promises a clipboard copy, but the action writes a file")
	}
	if !strings.Contains(out, ":curl") || !strings.Contains(out, ".curl file") {
		t.Error(":curl help must describe the .curl file it writes")
	}
}
```

- [ ] **Step 2: Run the test and verify it fails on the misleading wording**

```bash
go test ./internal/tui -run TestCurlCommandHelpDescribesAFileExport -count=1
```

Expected: FAIL because the command description currently says “copy.”

- [ ] **Step 3: Correct every user-facing description**

Change the command description to:

```go
desc: "write the selected flow as a .curl file"
```

Change `docs/keybindings.md` to:

```markdown
| `:curl` | write the selected flow to `flow-<id>.curl` |
```

Keep README and `docs/exporting.md` wording if they already say “export” or “writes”; change only language that implies the clipboard.

- [ ] **Step 4: Run focused tests and documentation search**

```bash
gofmt -w internal/tui/command.go internal/tui/help_test.go
go test ./internal/tui -run TestCurlCommandHelpDescribesAFileExport -count=1
rg -n 'copy the selected flow|clipboard' internal/tui docs README.md
```

Expected: test passes; search returns no claim that curl export copies to the clipboard.

- [ ] **Step 5: Commit the copy correction**

```bash
git add internal/tui/command.go internal/tui/help_test.go docs/keybindings.md README.md docs/exporting.md
git commit -m "docs: describe curl command as a file export"
```

If README and `docs/exporting.md` require no changes, omit them from `git add`.

---

### Task 4: Make Host Motions Follow the User-Facing Host Identity

**Files:**
- Modify: `internal/tui/command.go:193-230`
- Modify: `docs/keybindings.md:44-68`
- Test: `internal/tui/command_test.go:142-186`

**Interfaces:**
- Produces: `func flowHost(f *capture.Flow) string`
- Changes: `hostJump` compares `flowHost` values rather than raw `ServerAddr`

- [ ] **Step 1: Write failing host-identity tests**

Add cases proving that SNI and hostname—not endpoint port—define the user-facing group:

```go
func TestHostJumpUsesDisplayedHostIdentity(t *testing.T) {
	flows := []*capture.Flow{
		capture.NewFlow("c", "10.0.0.1:443"),
		capture.NewFlow("c", "10.0.0.2:8443"),
		capture.NewFlow("c", "api.example.com:80"),
		capture.NewFlow("c", "api.example.com:443"),
		capture.NewFlow("c", "other.example.com:443"),
	}
	flows[0].SNI = "api.example.com"
	flows[1].SNI = "api.example.com"

	if got := hostJump(flows, 0, +1, 1); got != 4 {
		t.Errorf("next displayed host from row 0 = %d, want 4", got)
	}
	if got := hostJump(flows, 4, -1, 1); got != 0 {
		t.Errorf("previous displayed host from row 4 = %d, want 0", got)
	}
}
```

This fixture treats all first four rows as `api.example.com`, whether that identity came from SNI or the requested hostname with different ports.

- [ ] **Step 2: Run the test and verify raw endpoint grouping fails**

```bash
go test ./internal/tui -run TestHostJumpUsesDisplayedHostIdentity -count=1
```

Expected: FAIL because current comparisons split the first four rows by raw `ServerAddr`.

- [ ] **Step 3: Implement the shared host identity**

Import `net` and add:

```go
func flowHost(f *capture.Flow) string {
	if f.SNI != "" {
		return f.SNI
	}
	host, _, err := net.SplitHostPort(f.ServerAddr)
	if err == nil {
		return host
	}
	return f.ServerAddr
}
```

In each `hostJump` loop, compare `flowHost(vis[j+1])`, `flowHost(vis[j-1])`, and `flowHost(vis[i])` instead of `ServerAddr`. Preserve the current backward-motion rule, count handling, clamping, and no-wrap behavior.

- [ ] **Step 4: Clarify host motion documentation**

In `docs/keybindings.md`, define host identity as:

```markdown
For `}` and `{`, a host is the TLS SNI name when present; otherwise it is the
requested hostname without its port. Consecutive flows for that host form one
jump group.
```

- [ ] **Step 5: Run focused and package tests**

```bash
gofmt -w internal/tui/command.go internal/tui/command_test.go
go test ./internal/tui -run 'TestHost(Motion|Jump)' -count=1
go test ./internal/tui
```

Expected: all pass.

- [ ] **Step 6: Commit host-motion behavior**

```bash
git add internal/tui/command.go internal/tui/command_test.go docs/keybindings.md
git commit -m "tui: group host motions by displayed hostname"
```

---

### Task 5: Prove Vim Keys Pass Through the Terminal Pane and Verify the Branch

**Files:**
- Modify: `internal/tui/command_test.go`
- No production changes expected.

**Interfaces:**
- Consumes: `runner.Target{Pty *os.File}`, `Model.onKey`, and `keyBytes`

- [ ] **Step 1: Add a pipe-backed terminal forwarding regression**

Add `io`, `os`, and `github.com/citizen-123/cli-capture/internal/runner` imports, then add:

```go
func TestVimKeysPassThroughTerminalPane(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEnd.Close()

	m := modelWithFlows(1)
	m.focus = focusTerminal
	m.target = &runner.Target{Pty: writeEnd}

	for _, typed := range []string{"5", "g", "G", ":", "{", "}"} {
		next, _ := m.onKey(key(typed))
		m = next.(Model)
	}
	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(readEnd)
	if err != nil {
		t.Fatal(err)
	}
	if want := "5gG:{}"; string(got) != want {
		t.Errorf("terminal received %q, want %q", got, want)
	}
	if m.count != 0 || m.pendingG || m.cmdline {
		t.Errorf("terminal keys changed vim state: count=%d pendingG=%v cmdline=%v",
			m.count, m.pendingG, m.cmdline)
	}
}
```

- [ ] **Step 2: Run the forwarding test**

```bash
gofmt -w internal/tui/command_test.go
go test ./internal/tui -run TestVimKeysPassThroughTerminalPane -count=1
```

Expected: PASS. This is characterization coverage for an existing security/UX boundary, not a production behavior change. To prove the test is meaningful, temporarily move motion parsing above the `focusTerminal` branch, confirm the test fails, then immediately restore `model.go` before continuing.

- [ ] **Step 3: Run complete verification**

```bash
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

Expected: all commands exit zero with no test, race, vet, or whitespace failures.

- [ ] **Step 4: Review the final diff against every finding**

Confirm:

- Space and `:flag` use the same shared operation and always preserve a valid selected row.
- Help hides unavailable `gg`, `G`, and counted-motion combinations.
- In-app command help displays every alias from `command.aliases`.
- `:curl` consistently says it writes a `.curl` file.
- Host jumps group by SNI/hostname rather than raw host-and-port endpoints.
- Documentation describes independent `g`, `G`, and digit override effects.
- The pipe-backed test proves Vim keys reach a focused target terminal unchanged.

- [ ] **Step 5: Commit final regression coverage**

```bash
git add internal/tui/command_test.go
git commit -m "test: prove vim keys pass through terminal focus"
```

- [ ] **Step 6: Request a fresh deep review**

Use `superpowers:requesting-code-review` with:

```text
Base: aac4b2e580b3d1eef64ff1e7e1dd686674cb3ee4
Head: the output of `git rev-parse HEAD` after Tasks 1-5
Requirements: every finding in docs/superpowers/plans/2026-07-29-vim-mode-review-findings.md
Focus: end-user UX, key/command parity, configured help accuracy, host grouping,
and terminal-pane byte transparency.
```

Resolve every Critical or Important finding before updating PR #16.
