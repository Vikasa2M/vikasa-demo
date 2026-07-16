// Command democtl drives the Phase-1 demo tour: verifying the pipeline is
// live (verify baseline), proving zero duplicates after redelivery (verify
// dedup), and simulating a WAN cut at one cabinet's leaf NATS (cut/restore)
// while the sim keeps buffering at the edge.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Vikasa2M/vikasa-demo/internal/ai"
	"github.com/Vikasa2M/vikasa-demo/internal/tour"
	"github.com/Vikasa2M/vikasa-demo/internal/verify"
)

const defaultComposeFile = "deploy/compose/docker-compose.yml"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "verify":
		runVerify(os.Args[2:])
	case "cut":
		runCut(os.Args[2:])
	case "restore":
		runRestore(os.Args[2:])
	case "tour":
		runTour(os.Args[2:])
	case "ai-setup":
		runAISetup(os.Args[2:])
	case "ai-reset":
		runAIReset(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "democtl: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  democtl verify baseline --dot <dot> [--ch <url>] [--expect-stale]
  democtl verify dedup --dot <dot> [--ch <url>]
  democtl verify ai-dashboard [--grafana <url>] [--ch <url>] [--user <user:pass>]
  democtl cut --cabinet <cabinet-id> [--compose-file <path>]
  democtl restore --cabinet <cabinet-id> [--compose-file <path>]
  democtl tour [--paced | --verify] [--ai] [--dot <dot>] [--only <phase1,phase2,...>]
               [--settle <duration>] [--ch <url>] [--grafana <url>]
               [--compose-file <path>] [--fleet <path>]
  democtl ai-setup [--grafana <url>] [--user <user:pass>] [--ch-host <host>] [--ch-port <port>]
  democtl ai-reset [--grafana <url>] [--user <user:pass>]`)
}

// --- verify ---

func runVerify(args []string) {
	if len(args) < 1 {
		usage()
		os.Exit(2)
	}
	switch args[0] {
	case "baseline":
		runVerifyBaseline(args[1:])
	case "dedup":
		runVerifyDedup(args[1:])
	case "ai-dashboard":
		runVerifyAIDashboard(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "democtl verify: unknown subcommand %q\n", args[0])
		usage()
		os.Exit(2)
	}
}

func runVerifyBaseline(args []string) {
	fs := flag.NewFlagSet("verify baseline", flag.ExitOnError)
	dot := fs.String("dot", "", "DOT short name, e.g. gdot (required)")
	ch := fs.String("ch", "http://localhost:8123", "ClickHouse HTTP base URL")
	expectStale := fs.Bool("expect-stale", false,
		"invert the heartbeat-freshness check: assert the gap IS stale (use during a cut)")
	_ = fs.Parse(args)

	if *dot == "" {
		fmt.Fprintln(os.Stderr, "democtl verify baseline: -dot is required")
		os.Exit(2)
	}

	deps := verify.Deps{CH: *ch, DB: verify.Database(*dot), ExpectStale: *expectStale}
	ctx, cancel := context.WithTimeout(context.Background(), verify.DefaultTimeout)
	defer cancel()

	results, err := verify.RunBaseline(ctx, deps)
	for _, r := range results {
		printResult(r)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "democtl verify baseline: %v\n", err)
		os.Exit(1)
	}
	if !allPass(results) {
		os.Exit(1)
	}
}

func runVerifyDedup(args []string) {
	fs := flag.NewFlagSet("verify dedup", flag.ExitOnError)
	dot := fs.String("dot", "", "DOT short name, e.g. gdot (required)")
	ch := fs.String("ch", "http://localhost:8123", "ClickHouse HTTP base URL")
	_ = fs.Parse(args)

	if *dot == "" {
		fmt.Fprintln(os.Stderr, "democtl verify dedup: -dot is required")
		os.Exit(2)
	}

	deps := verify.Deps{CH: *ch, DB: verify.Database(*dot)}
	ctx, cancel := context.WithTimeout(context.Background(), verify.DefaultTimeout)
	defer cancel()

	res, err := verify.RunDedup(ctx, deps)
	if err != nil {
		fmt.Fprintf(os.Stderr, "democtl verify dedup: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("events_raw (last 30m): count=%d distinct(ce_id)=%d diff=%d\n",
		res.Count, res.Distinct, res.Diff)
	if res.Diff != 0 {
		fmt.Printf("[FAIL] dedup: %d duplicate ce_id row(s) found\n", res.Diff)
		os.Exit(1)
	}
	fmt.Println("[PASS] dedup: zero duplicates")
}

// runVerifyAIDashboard is the AI segment's take-QA gate: find the newest
// dashboard in the "AI Built" folder, re-run every panel's ClickHouse query
// (after substituting Grafana's time macros) as the same restricted
// ai_readonly user the AI itself was scoped to, and print per-panel
// PASS/FAIL. Exits nonzero if any panel query errored or if every panel
// query returned zero rows -- either way, nothing worth recording landed.
func runVerifyAIDashboard(args []string) {
	fs := flag.NewFlagSet("verify ai-dashboard", flag.ExitOnError)
	grafana := fs.String("grafana", "http://localhost:3000", "Grafana base URL")
	user := fs.String("user", "", "Grafana admin user:pass (HTTP Basic Auth); empty relies on anonymous-admin dev mode")
	ch := fs.String("ch", "http://localhost:8123", "ClickHouse HTTP base URL")
	_ = fs.Parse(args)

	client := ai.NewClient(*grafana, *user)
	ctx, cancel := context.WithTimeout(context.Background(), ai.DefaultTimeout)
	defer cancel()

	results, err := ai.VerifyAIDashboard(ctx, client, *ch)
	for _, r := range results {
		status := "PASS"
		if !r.Pass {
			status = "FAIL"
		}
		fmt.Printf("[%s] panel %q (id=%d): %s\n", status, r.PanelTitle, r.PanelID, r.Detail)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "democtl verify ai-dashboard: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("[PASS] ai-dashboard: take-QA gate passed")
}

func printResult(r verify.CheckResult) {
	status := "PASS"
	if !r.Pass {
		status = "FAIL"
	}
	fmt.Printf("[%s] %s: %s\n", status, r.Name, r.Detail)
}

func allPass(results []verify.CheckResult) bool {
	for _, r := range results {
		if !r.Pass {
			return false
		}
	}
	return true
}

// --- tour ---

// verifyModeTimeout bounds an unattended `democtl tour` (verify mode) run:
// 5 phases, the longest settle being 90s, plus HTTP/docker overhead. Paced
// mode has no timeout at all — a presenter narrating live must never have
// the process die out from under them mid-recording while they're pausing
// between Enter presses.
const verifyModeTimeout = 15 * time.Minute

func runTour(args []string) {
	fs := flag.NewFlagSet("tour", flag.ExitOnError)
	paced := fs.Bool("paced", false, "presenter/recording mode: narrate, show the dashboard, and wait for Enter before/after each phase")
	verifyFlag := fs.Bool("verify", false, "explicit alias for the default unattended rehearsal mode (mutually exclusive with --paced)")
	aiFlag := fs.Bool("ai", false, "include the AI segment (phases 6-7: ai-build, ai-qa -- see demo/ai). Presenter-driven: narrates and gates on Enter in every mode, since an external MCP client does the actual work")
	dot := fs.String("dot", "gdot", "primary DOT for the cabinet-scoped phases (wan-cut/restore/fault/corridor); baseline always checks all 3 DOTs")
	ch := fs.String("ch", "http://localhost:8123", "ClickHouse HTTP base URL")
	grafana := fs.String("grafana", "http://localhost:3000", "Grafana base URL (for narration links)")
	composeFile := fs.String("compose-file", defaultComposeFile, "path to the compose file (cut/restore's network resolution)")
	fleetFile := fs.String("fleet", "deploy/topology/fleet.yaml", "path to fleet.yaml (sim inject/healthz port derivation)")
	settle := fs.Duration("settle", 0, "override every phase's settle duration (0 = use each phase's own default; verify mode only)")
	only := fs.String("only", "", "comma-separated phase names to run, e.g. --only=wan-cut,restore or (with --ai) ai-build,ai-qa -- for rehearsing one phase at a time")
	_ = fs.Parse(args)

	if *paced && *verifyFlag {
		fmt.Fprintln(os.Stderr, "democtl tour: --paced and --verify are mutually exclusive")
		os.Exit(2)
	}

	phases, err := tour.BuildPhases(tour.Config{
		CH:          *ch,
		Grafana:     *grafana,
		ComposeFile: *composeFile,
		FleetPath:   *fleetFile,
		DOT:         *dot,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "democtl tour: %v\n", err)
		os.Exit(1)
	}

	// --only names are split between the standard 5-phase table
	// (tour.FilterPhases) and, with --ai, the AI segment's own two phase
	// names (ai-build, ai-qa) -- a single flag, two different phase
	// mechanisms underneath (see internal/tour/ai.go's doc comment on why
	// the AI segment doesn't reuse Phase/Run).
	stdOnly, aiOnly, err := splitOnlyNames(*only, *aiFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "democtl tour: %v\n", err)
		os.Exit(2)
	}

	switch {
	case *only == "":
		// no --only: run every standard phase, and (with --ai) both AI
		// phases -- phases is already the full unfiltered table, and a nil
		// aiOnly map means RunAIPhases runs both.
		aiOnly = nil
	case len(stdOnly) > 0:
		phases, err = tour.FilterPhases(phases, stdOnly)
		if err != nil {
			fmt.Fprintf(os.Stderr, "democtl tour: %v\n", err)
			os.Exit(2)
		}
	default:
		// --only was given but named none of the 5 standard phases (e.g.
		// --ai --only ai-build): skip the standard table entirely.
		phases = nil
	}

	// Paced mode runs under context.Background() (no timeout): a presenter
	// narrating live over a silent recording must never be cut off by a
	// wall-clock deadline while they pause between phases. Verify mode is
	// unattended, so it gets a generous but real bound -- UNLESS --ai is
	// set: the AI segment always waits on a human (and, behind them, an
	// external LLM) regardless of --paced/--verify, so it needs the same
	// unbounded context paced mode gets.
	ctx := context.Background()
	if !*paced && !*aiFlag {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, verifyModeTimeout)
		defer cancel()
	}

	if err := tour.Run(ctx, phases, tour.RunOptions{Paced: *paced, Settle: *settle}); err != nil {
		fmt.Fprintf(os.Stderr, "democtl tour: %v\n", err)
		os.Exit(1)
	}
	if !*paced && len(phases) > 0 {
		fmt.Println("democtl tour: all phases PASS")
	}

	if *aiFlag {
		aiCfg := tour.AIConfig{Grafana: *grafana, CH: *ch, DOT: *dot}
		if err := tour.RunAIPhases(ctx, aiCfg, aiOnly, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "democtl tour: %v\n", err)
			os.Exit(1)
		}
	}
}

// splitOnlyNames splits a --only value (comma-separated phase names, e.g.
// "wan-cut,ai-build") into the standard 5-phase names (for
// tour.FilterPhases) and the AI segment's own phase names (ai-build/ai-qa,
// for tour.RunAIPhases), skipping blanks from stray commas/whitespace. It
// errors if an AI-segment name is given without --ai, since that combination
// would otherwise silently run nothing for that name.
func splitOnlyNames(only string, aiFlag bool) (stdOnly []string, aiOnly map[string]bool, err error) {
	if only == "" {
		return nil, nil, nil
	}
	aiOnly = map[string]bool{}
	for _, name := range strings.Split(only, ",") {
		name = strings.TrimSpace(name)
		switch name {
		case "":
			continue
		case tour.PhaseAIBuild, tour.PhaseAIQA:
			if !aiFlag {
				return nil, nil, fmt.Errorf("--only names the AI-segment phase %q; pass --ai to run it", name)
			}
			aiOnly[name] = true
		default:
			stdOnly = append(stdOnly, name)
		}
	}
	return stdOnly, aiOnly, nil
}

// --- ai-setup / ai-reset ---

// runAISetup creates (idempotently) the "AI Built" Grafana folder, the
// ai-dashboard-builder service account scoped to Editor on that folder
// only, and a fresh API token, then prints a ready-to-paste MCP client env
// block. Safe to re-run before every take (see docs/RUNBOOK.md) -- it never
// leaves duplicate folders/accounts behind, and always prints a WORKING
// token (Grafana never re-exposes a token's secret, so ai-setup rotates:
// delete-if-exists, then create fresh).
func runAISetup(args []string) {
	fs := flag.NewFlagSet("ai-setup", flag.ExitOnError)
	grafana := fs.String("grafana", "http://localhost:3000", "Grafana base URL")
	user := fs.String("user", "", "Grafana admin user:pass (HTTP Basic Auth); empty relies on anonymous-admin dev mode")
	chHost := fs.String("ch-host", "localhost", "ClickHouse host to print in the env block")
	chPort := fs.String("ch-port", "8123", "ClickHouse HTTP port to print in the env block")
	_ = fs.Parse(args)

	client := ai.NewClient(*grafana, *user)
	ctx, cancel := context.WithTimeout(context.Background(), ai.DefaultTimeout)
	defer cancel()

	result, err := ai.Setup(ctx, client, *grafana, *chHost, *chPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "democtl ai-setup: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("democtl ai-setup: %q folder + %q service account + token ready. Paste into your MCP client env:\n\n",
		ai.FolderTitle, ai.SAName)
	fmt.Print(result.EnvBlock())
}

// runAIReset deletes every dashboard in the "AI Built" folder -- a clean
// slate between takes, so `verify ai-dashboard` never grades a stale or
// half-built dashboard from a previous (possibly bad) take.
func runAIReset(args []string) {
	fs := flag.NewFlagSet("ai-reset", flag.ExitOnError)
	grafana := fs.String("grafana", "http://localhost:3000", "Grafana base URL")
	user := fs.String("user", "", "Grafana admin user:pass (HTTP Basic Auth); empty relies on anonymous-admin dev mode")
	_ = fs.Parse(args)

	client := ai.NewClient(*grafana, *user)
	ctx, cancel := context.WithTimeout(context.Background(), ai.DefaultTimeout)
	defer cancel()

	n, err := ai.Reset(ctx, client)
	if err != nil {
		fmt.Fprintf(os.Stderr, "democtl ai-reset: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("democtl ai-reset: deleted %d dashboard(s) from the %q folder\n", n, ai.FolderTitle)
}

// --- cut / restore ---

func runCut(args []string) {
	cabinet, composeFile := parseCabinetFlags("cut", args)
	if err := cutOrRestore(composeFile, cabinet, "disconnect"); err != nil {
		fmt.Fprintf(os.Stderr, "democtl cut: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("cut: %s leaf NATS disconnected from the compose network (sim keeps buffering at the edge)\n", cabinet)
}

func runRestore(args []string) {
	cabinet, composeFile := parseCabinetFlags("restore", args)
	if err := cutOrRestore(composeFile, cabinet, "connect"); err != nil {
		fmt.Fprintf(os.Stderr, "democtl restore: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("restore: %s leaf NATS reconnected to the compose network\n", cabinet)
}

// networkConnectArgs builds the `docker network connect`/`disconnect`
// argument list for container on network. A plain `docker network connect
// <network> <container>` re-attaches the container's IP but does NOT
// restore the compose-managed DNS alias (the service name) that other
// containers use to reach it — Compose registers that alias with
// `--alias <service>` at container-create time, and a bare reconnect drops
// it, leaving only the container's long/short name resolvable. Without the
// alias, `nats://<service>:4222` URLs baked into every other service's
// config (cabinet-sim's NATS_URL, regional's leafnode remote, etc.) fail to
// resolve even though the container is back on the network. `disconnect`
// takes no such flag.
func networkConnectArgs(action, network, containerName, service string) []string {
	args := []string{"network", action, network}
	if action == "connect" {
		args = append(args, "--alias", service)
	}
	args = append(args, containerName)
	return args
}

func parseCabinetFlags(name string, args []string) (cabinet, composeFile string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	c := fs.String("cabinet", "", "cabinet id, e.g. cab-i85-001 (required)")
	f := fs.String("compose-file", defaultComposeFile, "path to the compose file")
	_ = fs.Parse(args)
	if *c == "" {
		fmt.Fprintf(os.Stderr, "democtl %s: -cabinet is required\n", name)
		os.Exit(2)
	}
	return *c, *f
}

// cutOrRestore resolves the cabinet's LEAF NATS container (never the sim —
// see verify.CabinetLeafService) and the shared WAN-side "vikasa" network
// (verify.WANNetworkKey), then runs `docker network <action>` against that
// resolved name/network. Nothing is hardcoded: both are found via compose's
// own container/network labels, so a rename still resolves.
//
// Resolution uses lightweight `docker ps` / `docker network ls` label queries
// rather than `docker compose ps --format json`. Compose's project
// enumeration fires a heavy burst of API calls that, at ~100-cabinet scale
// (200+ containers), intermittently disconnects the Docker Desktop daemon —
// observed breaking cut/restore mid-tour, while plain `docker` calls stay a
// single cheap request each and never trip it. Resolving the network by its
// own compose label (not the target's Networks field) also means `restore`
// works after `cut` has already detached the target from "vikasa": the
// network name comes from the label, independent of who is currently on it.
//
// The leaf is also attached to its private per-cabinet LAN network (e.g.
// cab-i85-001-net) so the sim keeps buffering at the edge through a cut; only
// the "vikasa" attachment is disconnected/reconnected here. composeFile is
// retained in the signature for the CLI flag's backward compatibility but is
// no longer read.
func cutOrRestore(composeFile, cabinet, action string) error {
	_ = composeFile
	service := verify.CabinetLeafService(cabinet)

	container, err := dockerFirstLine("ps",
		"--filter", "label=com.docker.compose.service="+service, "--format", "{{.Names}}")
	if err != nil {
		return err
	}
	if container == "" {
		return fmt.Errorf("no running container for compose service %q", service)
	}
	network, err := dockerFirstLine("network", "ls",
		"--filter", "label=com.docker.compose.network="+verify.WANNetworkKey, "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	if network == "" {
		return fmt.Errorf("no compose network labeled %q found", verify.WANNetworkKey)
	}

	netArgs := networkConnectArgs(action, network, container, service)
	netCmd := exec.Command("docker", netArgs...)
	netCmd.Stdout = os.Stdout
	netCmd.Stderr = os.Stderr
	if err := netCmd.Run(); err != nil {
		return fmt.Errorf("docker %s: %w", strings.Join(netArgs, " "), err)
	}
	return nil
}

// dockerFirstLine runs `docker <args...>` and returns the first non-empty
// trimmed output line (or "" if there is none). Used to resolve a single
// container/network name from a label-filtered `docker ps`/`docker network ls`
// without the daemon-stressing weight of `docker compose ps` (see
// cutOrRestore).
func dockerFirstLine(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s, nil
		}
	}
	return "", nil
}
