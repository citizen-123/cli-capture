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
	"github.com/citizen-123/cli-capture/internal/intercept"
	"github.com/citizen-123/cli-capture/internal/proxy"
	"github.com/citizen-123/cli-capture/internal/proxy/ca"
	"github.com/citizen-123/cli-capture/internal/runner"
	"github.com/citizen-123/cli-capture/internal/scope"
	"github.com/citizen-123/cli-capture/internal/terminal"
	"github.com/citizen-123/cli-capture/internal/transparent"
	"github.com/citizen-123/cli-capture/internal/tui"
)

func main() {
	var (
		listen  = flag.String("listen", "127.0.0.1:0", "proxy listen address")
		confDir = flag.String("dir", defaultDir(), "config/CA directory")

		scopeInc = flag.String("scope", "", "intercept these (comma-separated specs; default: all). Spec: [!][field:]pattern, e.g. '*.github.com', 'path:/v1/*', 'method:=POST', 'host:~^api\\.'")
		scopeExc = flag.String("exclude", "", "never intercept these (comma-separated specs); wins over -scope")
		lastWins = flag.Bool("last-match", false, "evaluate all rules and take the last match (default: first match wins)")

		mitmInc = flag.String("mitm", "", "MITM only these TLS hosts (comma-separated specs; default: all)")
		mitmExc = flag.String("no-mitm", "", "pass these TLS hosts through without decrypting (comma-separated specs)")

		transparentAddr  = flag.String("transparent", "", "also run a transparent-redirect listener at this address (Linux, needs root+nftables); empty = off")
		transparentUID   = flag.Int("transparent-uid", -1, "uid whose traffic the nftables rule redirects")
		transparentApply = flag.Bool("transparent-apply", false, "actually install/remove the nftables redirect (needs root); otherwise the rules are only logged")

		loadPath = flag.String("load", "", "preload a saved capture session (JSON) into the flow list")

		leaderSpec = flag.String("leader", "ctrl+a", "tmux-style prefix key for cli-capture's own commands: ctrl+a … ctrl+z, or ctrl+space")
	)
	flag.Parse()
	argv := flag.Args()
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cli-capture [flags] -- <command> [args...]")
		os.Exit(2)
	}

	// Parse the leader before anything is spawned, so a typo fails on stderr
	// rather than after the TUI has taken the screen.
	leader, err := tui.ParseLeader(*leaderSpec)
	if err != nil {
		fatal("leader: %v", err)
	}

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
	env := runner.ProxyEnv(os.Environ(), px.Addr(), caFile)
	target, err := runner.Start(argv, env)
	if err != nil {
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
	model := tui.New(store, engine, target, screen, feeds, filepath.Join(*confDir, "session.json"), leader)
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
