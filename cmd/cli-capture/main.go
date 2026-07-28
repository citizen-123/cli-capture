// Command cli-capture launches a target CLI with its network traffic routed
// through an interception proxy, presenting a split-pane TUI: the target's
// terminal on the left, live/tamperable traffic on the right.
//
//	cli-capture -- curl https://example.com
//	cli-capture -scope api.github.com -- gh pr list
package main

import (
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
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("cli-capture %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	argv := flag.Args()
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cli-capture [flags] -- <command> [args...]")
		os.Exit(2)
	}

	// Log to a file, not the screen — the TUI owns stdout.
	logf, _ := os.Create(filepath.Join(*confDir, "cli-capture.log"))
	if logf != nil {
		log.SetOutput(logf)
		defer logf.Close()
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
	palette, err := theme.Resolve(cfg.Theme.Base, cfg.Theme.Colors)
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
	env := runner.ProxyEnv(os.Environ(), px.Addr(), caFile)
	target, err := runner.Start(argv, env)
	if err != nil {
		fatal("start target: %v", err)
	}
	defer target.Close()

	// Feeds bridging the plumbing to the UI's Elm loop. A full VT emulator (not
	// the line buffer) so full-screen TUI targets render correctly; its query
	// replies go back to the target's PTY input.
	screen := terminal.NewVT(80, 24, target.Pty)
	ptyCh := make(chan struct{}, 1)
	pauseCh := make(chan tui.Paused, 16)

	engine.OnPause(func(f *capture.Flow, msg *capture.Message) {
		pauseCh <- tui.Paused{Flow: f, Msg: msg}
	})

	go pumpPTY(target, screen, ptyCh)

	feeds := tui.Feeds{Events: store.Subscribe(), Pty: ptyCh, Pause: pauseCh}
	model := tui.New(store, engine, target, screen, feeds, filepath.Join(*confDir, "session.json")).WithKeys(keys)
	prog := tea.NewProgram(model, tea.WithAltScreen())

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
