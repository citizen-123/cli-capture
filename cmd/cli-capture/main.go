// Command cli-capture launches a target CLI with its network traffic routed
// through an interception proxy, presenting a split-pane TUI: the target's
// terminal on the left, live/tamperable traffic on the right.
//
//	cli-capture -- curl https://example.com
//	cli-capture -scope api.github.com -- gh pr list
//	cli-capture                            # bare: capture a whole $SHELL session
//	cli-capture -shell -- claude-as alt    # target is a shell alias or function
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/citizen-123/cli-capture/internal/capture"
	"github.com/citizen-123/cli-capture/internal/config"
	"github.com/citizen-123/cli-capture/internal/intercept"
	"github.com/citizen-123/cli-capture/internal/proxy"
	"github.com/citizen-123/cli-capture/internal/proxy/ca"
	"github.com/citizen-123/cli-capture/internal/runner"
	"github.com/citizen-123/cli-capture/internal/scope"
	"github.com/citizen-123/cli-capture/internal/terminal"
	"github.com/citizen-123/cli-capture/internal/theme"
	"github.com/citizen-123/cli-capture/internal/transparent"
	"github.com/citizen-123/cli-capture/internal/tui"
)

// Build metadata, overridden at release time via -ldflags "-X main.version=...".
// GoReleaser populates these by default (see .goreleaser.yaml).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	var (
		showVersion = flag.Bool("version", false, "print version and exit")

		listen  = flag.String("listen", "127.0.0.1:0", "proxy listen address")
		confDir = flag.String("dir", defaultDir(), "data directory: CA, sessions, exports, log")

		themeName = flag.String("theme", "", "color theme: "+strings.Join(theme.Names(), " | ")+" (default "+theme.Default+")")
	)

	// -config may be repeated and/or comma-separated; every file merges in the
	// order given, on top of the default config.
	var configs config.Paths
	flag.Var(&configs, "config", "config file(s) or preset name(s) in "+config.Dir()+"; repeatable, comma-separated")

	var (
		scopeInc = flag.String("scope", "", "intercept these (comma-separated specs; default: all). Spec: [!][field:]pattern, e.g. '*.github.com', 'path:/v1/*', 'method:=POST', 'host:~^api\\.'")
		scopeExc = flag.String("exclude", "", "never intercept these (comma-separated specs); wins over -scope")
		lastWins = flag.Bool("last-match", false, "evaluate all rules and take the last match (default: first match wins)")

		mitmInc = flag.String("mitm", "", "MITM only these TLS hosts (comma-separated specs; default: all)")
		mitmExc = flag.String("no-mitm", "", "pass these TLS hosts through without decrypting (comma-separated specs)")

		transparentAddr  = flag.String("transparent", "", "also run a transparent-redirect listener at this address (Linux, needs root+nftables); empty = off")
		transparentUID   = flag.Int("transparent-uid", -1, "uid whose traffic the nftables rule redirects")
		transparentApply = flag.Bool("transparent-apply", false, "actually install/remove the nftables redirect (needs root); otherwise the rules are only logged")

		loadPath = flag.String("load", "", "preload a saved capture session (JSON) into the flow list")

		useShell = flag.Bool("shell", false, "run the target through $SHELL -ic so aliases and shell functions resolve")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("cli-capture %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	argv := flag.Args()
	// Whether the user named a target decides which remedy an unresolvable argv
	// gets later: a named target may be an alias, a derived one means $SHELL is
	// broken.
	namedTarget := len(argv) > 0
	if len(argv) == 0 {
		if *useShell {
			fatal("-shell needs a command: cli-capture -shell -- <command> [args...]")
		}
		argv = runner.LoginShell() // bare launch: an interactive shell as the target
	} else if *useShell {
		argv = runner.ShellCommand(argv)
	}

	// User configuration: theme and keymap. Loaded before anything renders, and
	// fatal on error — a config that half-applied would be worse than a refusal.
	cfg, err := config.Load(configs, *confDir)
	if err != nil {
		fatal("%v", err)
	}
	log.Printf("config: %s", cfg.Describe())

	if *themeName != "" {
		cfg.Theme.Base = *themeName // the flag wins over the file
	}
	palette, err := theme.Resolve(cfg.Theme.Base, cfg.Theme.Colors, cfg.Theme.Glyphs, cfg.Theme.Border)
	if err != nil {
		fatal("%v", err)
	}
	if os.Getenv("NO_COLOR") != "" {
		palette = palette.Colorless()
		log.Printf("theme: %s (NO_COLOR set — colors stripped)", palette.Name)
	} else {
		log.Printf("theme: %s", palette.Name)
	}
	tui.ApplyTheme(palette)

	keys, err := tui.NewKeyMap(cfg.Keys.Leader, cfg.Keys.Bindings)
	if err != nil {
		fatal("%v", err)
	}
	log.Printf("keys: leader %s", keys.LeaderName)

	authority, err := ca.LoadOrCreate(*confDir)
	if err != nil {
		fatal("ca: %v", err)
	}

	// Log to a file, not the screen — the TUI owns stdout. This has to come
	// after ca.LoadOrCreate: that is what creates confDir, so opening the log
	// any earlier fails on a first run and every log line lands on the TUI.
	logf, err := os.Create(filepath.Join(*confDir, "cli-capture.log"))
	if err != nil {
		fatal("open log %s: %v", filepath.Join(*confDir, "cli-capture.log"), err)
	}
	log.SetOutput(logf)
	defer logf.Close()
	caFile := filepath.Join(*confDir, "ca.pem")

	store := capture.NewStore()
	if *loadPath != "" {
		flows, err := capture.LoadFile(*loadPath)
		if err != nil {
			fatal("load session: %v", err)
		}
		for _, f := range flows {
			store.Add(f)
		}
		log.Printf("preloaded %d flows from %s", len(flows), *loadPath)
	}
	engine := intercept.NewEngine()

	// Interception scope: an allowlist when -scope is given, otherwise
	// intercept-all (excludes still apply). Excludes always win over includes.
	interceptScope, err := buildScope(*scopeInc, *scopeExc, *lastWins)
	if err != nil {
		fatal("scope: %v", err)
	}
	engine.SetScope(interceptScope)
	log.Printf("intercept scope: %s", interceptScope.Describe())

	// Transport engine.
	px := proxy.New(store, authority, engine)

	// MITM policy: MITM everything unless -mitm restricts it; -no-mitm passes
	// listed TLS hosts through blind.
	mitmScope, err := buildScope(*mitmInc, *mitmExc, *lastWins)
	if err != nil {
		fatal("mitm policy: %v", err)
	}
	px.SetMITMPolicy(mitmScope)
	log.Printf("mitm policy: %s", mitmScope.Describe())

	if err := px.Listen(*listen); err != nil {
		fatal("proxy listen: %v", err)
	}
	go px.Serve()
	defer px.Close()
	log.Printf("proxy listening on %s", px.Addr())

	// Optional transparent-redirect capture for targets that ignore proxy env.
	if *transparentAddr != "" {
		tl, err := transparent.Listen(*transparentAddr)
		if err != nil {
			fatal("transparent listen: %v", err)
		}
		defer tl.Close()
		go tl.Serve(func(conn net.Conn, dst string) { px.HandleTransparent(conn, dst) })

		if *transparentApply {
			if *transparentUID < 0 {
				fatal("transparent: -transparent-apply requires -transparent-uid")
			}
			_, portStr, _ := net.SplitHostPort(tl.Addr())
			port, _ := strconv.Atoi(portStr)
			teardown, backend, err := transparent.ApplyRedirect(port, *transparentUID)
			if err != nil {
				fatal("transparent apply: %v", err)
			}
			defer teardown()
			log.Printf("transparent: applied via %s", backend)
			// Flush the rules on SIGINT/SIGTERM too — a leftover NAT redirect is
			// the worst failure mode. (A hard SIGKILL can't be caught; the next
			// -transparent-apply run flushes any stale table first.)
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				_ = teardown()
				os.Exit(1)
			}()
			log.Printf("transparent: nftables redirect applied (uid %d → %s)", *transparentUID, tl.Addr())
		} else {
			logTransparentSetup(tl, *transparentUID)
		}
	}

	// Launch the target under a PTY with its env rewritten to route through us.
	// Log the resolved argv: with a bare launch or -shell it is derived rather
	// than typed, so the log is the only record of what actually ran.
	log.Printf("target: %s", strings.Join(argv, " "))
	env := runner.ProxyEnv(os.Environ(), px.Addr(), caFile)
	target, err := runner.Start(argv, env)
	if err != nil {
		// A name that doesn't resolve is usually an alias or shell function, so
		// point at the flag that handles those rather than leaving the user with
		// a bare lookup failure. A derived target can only have come from
		// $SHELL, where that advice would be nonsense.
		if errors.Is(err, runner.ErrTargetNotFound) {
			if namedTarget {
				fatal("start target: %v — if it is a shell alias or function, re-run with -shell (or launch a bare shell: cli-capture)", err)
			}
			fatal("start target: %v — $SHELL does not name an executable; fix it, or pass a target: cli-capture -- <command>", err)
		}
		fatal("start target: %v", err)
	}
	defer target.Close()

	// Feeds bridging the plumbing to the UI's Elm loop. A full VT emulator so
	// full-screen TUI targets render correctly; its query replies (and
	// forwarded mouse events) go back to the target's PTY input. Closing it
	// stops its reply-pump goroutine; do that before target.Close() (which
	// runs first, since defers are LIFO) so nothing writes to the PTY after
	// it's gone.
	screen := terminal.NewVT(80, 24, target.Pty)
	defer func() {
		if err := screen.Close(); err != nil {
			log.Printf("Encountered %v while closing the terminal emulator.", err)
		}
	}()
	ptyCh := make(chan struct{}, 1)
	pauseCh := make(chan tui.Paused, 16)

	engine.OnPause(func(f *capture.Flow, msg *capture.Message) {
		pauseCh <- tui.Paused{Flow: f, Msg: msg}
	})

	go pumpPTY(target, screen, ptyCh)

	feeds := tui.Feeds{Events: store.Subscribe(), Pty: ptyCh, Pause: pauseCh}
	model := tui.New(store, engine, target, screen, feeds, filepath.Join(*confDir, "session.json")).WithKeys(keys)
	// WithMouseCellMotion enables mouse reporting; the model's mouseCapture
	// toggle (leader m) flips it at runtime for native text selection.
	prog := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())

	// Quit the UI when the child exits.
	go func() {
		_ = target.Wait()
		prog.Quit()
	}()

	if _, err := prog.Run(); err != nil {
		fatal("tui: %v", err)
	}
}

// pumpPTY copies child output into the terminal screen and nudges the UI to
// re-render. The nudge is coalesced (non-blocking send into a size-1 channel)
// so a chatty child can't outpace the render loop.
func pumpPTY(target *runner.Target, screen terminal.Emulator, notify chan<- struct{}) {
	buf := make([]byte, 32*1024)
	for {
		n, err := target.Pty.Read(buf)
		if n > 0 {
			screen.Write(buf[:n])
			select {
			case notify <- struct{}{}:
			default:
			}
		}
		if err != nil {
			return
		}
	}
}

// buildScope assembles a scope.Set from comma-separated include/exclude specs.
// When no includes are given the default action is Include (match everything
// not excluded — a denylist); when includes are present the default flips to
// Exclude (match only what is included — an allowlist). Excludes are ordered
// first so they win over overlapping includes.
func buildScope(includes, excludes string, lastWins bool) (*scope.Set, error) {
	inc := splitComma(includes)
	exc := splitComma(excludes)
	set, err := scope.Build(inc, exc, len(inc) == 0)
	if err != nil {
		return nil, err
	}
	set.LastMatchWins = lastWins
	return set, nil
}

func splitComma(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// logTransparentSetup records the nftables commands the user must run (as root)
// to redirect their target's traffic into the transparent listener. It goes to
// the log file because the TUI owns the screen; the privileged setup is
// inherently a separate, out-of-band step.
func logTransparentSetup(tl *transparent.Listener, uid int) {
	_, portStr, _ := net.SplitHostPort(tl.Addr())
	port, _ := strconv.Atoi(portStr)

	// Show commands for whichever backend is available, defaulting to nft.
	bin, redirect, flush := "nft", transparent.NFTRedirect(port, uid), transparent.NFTFlush()
	if be, err := transparent.DetectBackend(); err == nil {
		bin, redirect, flush = be.Bin, be.Redirect(port, uid), be.Flush()
	}

	log.Printf("transparent: listening on %s (needs root + %s to redirect)", tl.Addr(), bin)
	if uid < 0 {
		log.Printf("transparent: pass -transparent-uid <uid> and run the target as that uid; rules:")
	} else {
		log.Printf("transparent: as root, redirect uid %d with:", uid)
	}
	for _, c := range transparent.Shell(bin, redirect) {
		log.Printf("  %s", c)
	}
	log.Printf("transparent: teardown: %s", strings.Join(transparent.Shell(bin, flush), " && "))
}

func defaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".cli-capture"
	}
	return filepath.Join(home, ".cli-capture")
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
