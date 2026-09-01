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

var errInterrupted = errors.New("interrupted")

func main() {
	if err := run(); err != nil {
		if errors.Is(err, errInterrupted) {
			os.Exit(1)
		}
		fatal("%v", err)
	}
}

func run() error {
	var (
		showVersion = flag.Bool("version", false, "print version and exit")

		listen  = flag.String("listen", "127.0.0.1:0", "proxy listen address")
		confDir = flag.String("dir", defaultDir(), "data directory: CA, sessions, exports, log")

		themeName = flag.String("theme", "", "color theme: "+strings.Join(theme.Names(), " | ")+" (default "+theme.Default+")")
		leaderKey = flag.String("leader", "", "prefix key for cli-capture's own commands: ctrl+a … ctrl+z (default "+tui.DefaultLeader+"). Move it if your target app needs the default")
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
		transparentUID   = flag.Int("transparent-uid", -1, "uid assigned to the target and redirected when -transparent-apply is used")
		transparentApply = flag.Bool("transparent-apply", false, "actually install/remove the nftables redirect (needs root); otherwise the rules are only logged")

		loadPath = flag.String("load", "", "preload a saved capture session (JSON) into the flow list")

		useShell = flag.Bool("shell", false, "run the target through $SHELL -ic so aliases and shell functions resolve")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("cli-capture %s (commit %s, built %s)\n", version, commit, date)
		return nil
	}

	argv := flag.Args()
	// Whether the user named a target decides which remedy an unresolvable argv
	// gets later: a named target may be an alias, a derived one means $SHELL is
	// broken.
	namedTarget := len(argv) > 0
	if len(argv) == 0 {
		if *useShell {
			return fmt.Errorf("-shell needs a command: cli-capture -shell -- <command> [args...]")
		}
		argv = runner.LoginShell() // bare launch: an interactive shell as the target
	} else if *useShell {
		argv = runner.ShellCommand(argv)
	}

	// Establish the private diagnostics destination before configuration emits
	// anything. fatal writes directly to stderr, so failures opening this file
	// and later startup validation errors remain visible to the operator.
	logPath := filepath.Join(*confDir, "cli-capture.log")
	logf, err := openStartupLog(*confDir)
	if err != nil {
		return fmt.Errorf("open log %s: %v", logPath, err)
	}
	log.SetOutput(logf)
	defer logf.Close()

	// User configuration: theme and keymap. Loaded before anything renders, and
	// fatal on error — a config that half-applied would be worse than a refusal.
	cfg, err := config.Load(configs, *confDir)
	if err != nil {
		return fmt.Errorf("%v", err)
	}
	log.Printf("config: %s", cfg.Describe())

	if *themeName != "" {
		cfg.Theme.Base = *themeName // the flag wins over the file
	}
	palette, err := theme.Resolve(cfg.Theme.Base, cfg.Theme.Colors, cfg.Theme.Glyphs, cfg.Theme.Border)
	if err != nil {
		return fmt.Errorf("%v", err)
	}
	if os.Getenv("NO_COLOR") != "" {
		palette = palette.Colorless()
		log.Printf("theme: %s (NO_COLOR set — colors stripped)", palette.Name)
	} else {
		log.Printf("theme: %s", palette.Name)
	}
	tui.ApplyTheme(palette)

	if *leaderKey != "" {
		cfg.Keys.Leader = *leaderKey // the flag wins over the file
	}
	keys, err := tui.NewKeyMap(cfg.Keys.Leader, cfg.Keys.Bindings)
	if err != nil {
		return fmt.Errorf("%v", err)
	}
	log.Printf("keys: leader %s", keys.LeaderName)

	authority, err := ca.LoadOrCreate(*confDir)
	if err != nil {
		return fmt.Errorf("ca: %v", err)
	}
	caFile := filepath.Join(*confDir, "ca.pem")

	store := capture.NewStore()
	if *loadPath != "" {
		flows, err := capture.LoadFile(*loadPath)
		if err != nil {
			return fmt.Errorf("load session: %v", err)
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
		return fmt.Errorf("scope: %v", err)
	}
	engine.SetScope(interceptScope)
	log.Printf("intercept scope: %s", interceptScope.Describe())

	// Transport engine.
	px := proxy.New(store, authority, engine)

	// MITM policy: MITM everything unless -mitm restricts it; -no-mitm passes
	// listed TLS hosts through blind.
	mitmScope, err := buildScope(*mitmInc, *mitmExc, *lastWins)
	if err != nil {
		return fmt.Errorf("mitm policy: %v", err)
	}
	px.SetMITMPolicy(mitmScope)
	log.Printf("mitm policy: %s", mitmScope.Describe())

	var (
		targetCredentials *runner.UserCredentials
		target            *runner.Target
		screen            terminal.Emulator
		prog              *tea.Program
	)
	return executeCapture(captureLifecycle{
		startProxy: func() error {
			if err := px.Listen(*listen); err != nil {
				return fmt.Errorf("proxy listen: %v", err)
			}
			go px.Serve()
			log.Printf("proxy listening on %s", px.Addr())
			return nil
		},
		closeProxy: func() { _ = px.Close() },
		startTransparent: func() (transparentLifecycle, error) {
			targetCredentials, err = transparentTargetCredentials(*transparentAddr, *transparentApply, *transparentUID)
			if err != nil {
				return transparentLifecycle{}, fmt.Errorf("%v", err)
			}

			if *transparentAddr == "" {
				return transparentLifecycle{}, nil
			}

			tl, err := transparent.Listen(*transparentAddr)
			if err != nil {
				return transparentLifecycle{}, fmt.Errorf("transparent listen: %v", err)
			}
			lifecycle := transparentLifecycle{
				closeListener: func() { _ = tl.Close() },
			}
			go tl.Serve(func(conn net.Conn, dst string) { px.HandleTransparent(conn, dst) })

			if !*transparentApply {
				logTransparentSetup(tl, *transparentUID)
				return lifecycle, nil
			}

			_, portStr, _ := net.SplitHostPort(tl.Addr())
			port, _ := strconv.Atoi(portStr)
			teardown, backend, err := transparent.ApplyRedirect(port, *transparentUID)
			if err != nil {
				return lifecycle, fmt.Errorf("transparent apply: %v", err)
			}
			lifecycle.teardown = func() { _ = teardown() }
			log.Printf("transparent: applied via %s", backend)

			// Route termination through run's return boundary so every resource
			// unwinds in LIFO order before main preserves the signal exit status.
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
			lifecycle.signals = ch
			lifecycle.stopSignals = func() { signal.Stop(ch) }
			log.Printf("transparent: nftables redirect applied (uid %d → %s)", *transparentUID, tl.Addr())
			return lifecycle, nil
		},
		startTarget: func() error {
			// Log the resolved argv: with a bare launch or -shell it is derived
			// rather than typed, so the log records what actually ran.
			log.Printf("target: %s", strings.Join(argv, " "))
			env := runner.ProxyEnv(os.Environ(), px.Addr(), caFile)
			target, err = startTarget(argv, env, targetCredentials, runner.Start, runner.StartWithCredentials)
			if err == nil {
				return nil
			}
			if errors.Is(err, runner.ErrTargetNotFound) {
				if namedTarget {
					return fmt.Errorf("start target: %v — if it is a shell alias or function, re-run with -shell (or launch a bare shell: cli-capture)", err)
				}
				return fmt.Errorf("start target: %v — $SHELL does not name an executable; fix it, or pass a target: cli-capture -- <command>", err)
			}
			return fmt.Errorf("start target: %v", err)
		},
		closeTarget: func() { _ = target.Close() },
		startTerminal: func() {
			// Closing the emulator stops its reply pump before target.Close
			// releases the PTY; executeCapture registers them in that order.
			screen = terminal.NewVT(80, 24, target.Pty)
			ptyCh := make(chan struct{}, 1)
			pauseCh := make(chan tui.Paused, 16)
			engine.OnPause(func(token intercept.PauseToken, f *capture.Flow, msg *capture.Message) {
				pauseCh <- tui.Paused{Token: token, Flow: f, Msg: msg}
			})
			go pumpPTY(target, screen, ptyCh)

			feeds := tui.Feeds{Events: store.Subscribe(), Pty: ptyCh, Pause: pauseCh}
			model := tui.New(store, engine, target, screen, feeds, filepath.Join(*confDir, "session.json")).WithKeys(keys)
			prog = tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
			go func() {
				_ = target.Wait()
				prog.Quit()
			}()
		},
		closeTerminal: func() {
			if err := screen.Close(); err != nil {
				log.Printf("Encountered %v while closing the terminal emulator.", err)
			}
		},
		runProgram: func(sigCh <-chan os.Signal) error {
			return runProgram(
				func() error {
					_, err := prog.Run()
					return err
				},
				prog.Quit,
				sigCh,
			)
		},
	})
}

type captureLifecycle struct {
	startProxy       func() error
	closeProxy       func()
	startTransparent func() (transparentLifecycle, error)
	startTarget      func() error
	closeTarget      func()
	startTerminal    func()
	closeTerminal    func()
	runProgram       func(<-chan os.Signal) error
}

type transparentLifecycle struct {
	closeListener func()
	teardown      func()
	signals       <-chan os.Signal
	stopSignals   func()
}

func executeCapture(lifecycle captureLifecycle) error {
	if err := lifecycle.startProxy(); err != nil {
		return err
	}
	defer lifecycle.closeProxy()

	transparentState, err := lifecycle.startTransparent()
	if transparentState.closeListener != nil {
		defer transparentState.closeListener()
	}
	if transparentState.teardown != nil {
		defer transparentState.teardown()
	}
	if transparentState.stopSignals != nil {
		defer transparentState.stopSignals()
	}
	if err != nil {
		return err
	}

	if err := lifecycle.startTarget(); err != nil {
		return err
	}
	defer lifecycle.closeTarget()

	lifecycle.startTerminal()
	defer lifecycle.closeTerminal()

	return lifecycle.runProgram(transparentState.signals)
}

func runProgram(run func() error, quit func(), sigCh <-chan os.Signal) error {
	if sigCh == nil {
		if err := run(); err != nil {
			return fmt.Errorf("tui: %w", err)
		}
		return nil
	}

	interrupted := make(chan struct{}, 1)
	done := make(chan struct{})
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		select {
		case <-sigCh:
			quit()
			interrupted <- struct{}{}
		case <-done:
		}
	}()

	runErr := run()
	close(done)
	<-handlerDone
	if runErr != nil {
		return fmt.Errorf("tui: %w", runErr)
	}
	select {
	case <-interrupted:
		return errInterrupted
	default:
		return nil
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

// transparentTargetCredentials selects a child identity only for automatic
// transparent-rule application. Manual rules and ordinary proxy mode leave the
// target's credentials untouched.
func transparentTargetCredentials(addr string, apply bool, uid int) (*runner.UserCredentials, error) {
	return transparentTargetCredentialsWithLookup(addr, apply, uid, runner.LookupUserCredentials)
}

func transparentTargetCredentialsWithLookup(
	addr string,
	apply bool,
	uid int,
	lookup func(int) (*runner.UserCredentials, error),
) (*runner.UserCredentials, error) {
	if addr == "" || !apply {
		return nil, nil
	}
	if uid < 0 {
		return nil, fmt.Errorf("transparent: -transparent-apply requires -transparent-uid")
	}
	if uid == 0 {
		return nil, fmt.Errorf("transparent: -transparent-uid must name a non-root user so proxy traffic is not redirected")
	}
	credentials, err := lookup(uid)
	if err != nil {
		return nil, fmt.Errorf("transparent: resolve -transparent-uid %d: %w", uid, err)
	}
	return credentials, nil
}

type targetStarter func([]string, []string) (*runner.Target, error)
type credentialTargetStarter func([]string, []string, *runner.UserCredentials) (*runner.Target, error)

func startTarget(
	argv []string,
	env []string,
	credentials *runner.UserCredentials,
	start targetStarter,
	startWithCredentials credentialTargetStarter,
) (*runner.Target, error) {
	if credentials != nil {
		return startWithCredentials(argv, env, credentials)
	}
	return start(argv, env)
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

// openStartupLog creates the data directory before the CA needs it, narrows an
// existing directory just as ca.LoadOrCreate does, and appends so an early
// validation failure cannot destroy prior diagnostics. The explicit chmod also
// repairs a log created by an older version with a permissive Unix mode;
// OpenFile's mode applies only when the file is new.
func openStartupLog(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "cli-capture.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
