// This file implements tour phases 6-7 -- the AI "models-only" dashboard
// segment (Task 19), gated behind `democtl tour --ai`. It deliberately does
// NOT reuse the Phase/Run (action/settle/assert, paced-vs-verify) machinery
// phases.go and tour.go build for phases 1-5: those five phases' whole
// design rests on democtl being able to trigger and observe every effect
// itself (a cut, an injected fault, a ClickHouse query), so paced mode can
// skip assertions (the presenter watches the dashboard) and verify mode can
// run unattended (democtl asserts for them).
//
// The AI segment breaks that assumption at its root: the actual work (an
// LLM exploring ClickHouse and building a dashboard over MCP, then
// answering ad hoc questions) happens in an EXTERNAL MCP client democtl can
// neither see nor drive. There is no "verify mode" version of "have a
// frontier model build a dashboard" -- a human must always drive that part,
// in every mode. So both phases always narrate AND always gate on the
// operator's Enter, and the one piece that DOES run automatically either
// way is the take-QA gate (`verify ai-dashboard`) once the operator
// confirms the AI is done -- a bad take must be caught immediately, not
// discovered after the Q&A beat is already in the can.
package tour

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

// Phase names for the AI segment, filterable via `democtl tour --ai --only
// ai-build,ai-qa` the same way phases 1-5 use --only.
const (
	PhaseAIBuild = "ai-build"
	PhaseAIQA    = "ai-qa"
)

// AIConfig parameterizes tour phases 6-7: where to find Grafana/ClickHouse
// for the automated take-QA gate and phase 7's ground-truth `verify dedup`,
// and which prompts file to walk the operator through.
type AIConfig struct {
	Grafana     string // Grafana base URL, e.g. http://localhost:3000
	CH          string // ClickHouse HTTP base URL, e.g. http://localhost:8123
	DOT         string // DOT whose outage window phase 7's ground-truth dedup check targets
	PromptsPath string // path to demo/ai/prompts/questions.md
}

func (c AIConfig) withDefaults() AIConfig {
	if c.Grafana == "" {
		c.Grafana = "http://localhost:3000"
	}
	if c.CH == "" {
		c.CH = "http://localhost:8123"
	}
	if c.DOT == "" {
		c.DOT = "mardot"
	}
	if c.PromptsPath == "" {
		c.PromptsPath = "demo/ai/prompts/questions.md"
	}
	return c
}

// runFunc is the shape of the external democtl commands the AI segment
// shells out to (ai-setup, verify ai-dashboard, verify dedup) -- overridable
// in tests so they never need a live Grafana/ClickHouse stack or to exec a
// real democtl binary. RunAIPhases wires this to runDemoctl (see exec.go).
type runFunc func(ctx context.Context, args ...string) error

// RunAIPhases drives tour phases 6 (ai-build) and 7 (ai-qa) against cfg. If
// only is non-empty, it restricts which phase(s) run (keys among
// PhaseAIBuild/PhaseAIQA); nil or empty runs both, in order. in/out default
// to os.Stdin/os.Stdout when nil.
//
// Returns a non-nil error only when phase 6's take-QA gate fails (`verify
// ai-dashboard` reports FAIL) -- the caller should treat that as a bad take
// (exit nonzero) so it's never mistaken for a clean recording. Phase 7 has
// no PASS/FAIL notion (there's nothing to assert -- the AI's answers are
// judged live by the presenter against the printed ground truth), so it
// never contributes to the returned error.
func RunAIPhases(ctx context.Context, cfg AIConfig, only map[string]bool, in io.Reader, out io.Writer) error {
	cfg = cfg.withDefaults()
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	return runAIPhases(ctx, cfg, only, bufio.NewReader(in), out, runDemoctl)
}

func runAIPhases(ctx context.Context, cfg AIConfig, only map[string]bool, reader *bufio.Reader, out io.Writer, run runFunc) error {
	runBuild := len(only) == 0 || only[PhaseAIBuild]
	runQA := len(only) == 0 || only[PhaseAIQA]

	if runBuild {
		if err := runAIBuildPhase(ctx, cfg, reader, out, run); err != nil {
			return err
		}
	}
	if runQA {
		runAIQAPhase(ctx, cfg, reader, out, run)
	}
	return nil
}

const aiBuildNarration = `
This segment hands the AI ONLY the OpenITS YANG models -- no schema docs,
no dashboard to copy from. It discovers the ClickHouse schema itself over
MCP (a read-only ClickHouse server plus a scoped Grafana server) and builds
a dashboard from first principles. Steps:

  1. Run 'democtl ai-setup' (below) -- creates the "AI Built" Grafana
     folder, a scoped service account, and a fresh API token.
  2. Paste the printed env block into your MCP client's grafana + clickhouse
     server config (see demo/ai/mcp/README.md).
  3. Attach demo/ai/models-pack.generated.yang to the chat (make ai-models-pack
     if it's stale).
  4. Send demo/ai/prompts/system-models-only.md as the system prompt, then
     demo/ai/prompts/task-corridor.md as the task.
  5. Let the AI explore, correlate, and build. Watch it work in Grafana.`

// runAIBuildPhase narrates phase 6, runs `ai-setup` (deterministic --
// democtl can do this part itself), waits for the operator to drive the
// external MCP client and confirm the AI is done, then runs the take-QA
// gate (`verify ai-dashboard`) and reports PASS/FAIL right there -- a bad
// take must be caught before the presenter moves on to phase 7 or ends the
// recording.
func runAIBuildPhase(ctx context.Context, cfg AIConfig, reader *bufio.Reader, out io.Writer, run runFunc) error {
	fmt.Fprint(out, "\n================ Phase 6/7: ai-build (\"the model is all you need\") ================\n\n")
	fmt.Fprintln(out, strings.TrimSpace(aiBuildNarration))
	fmt.Fprintln(out)

	if err := run(ctx, "ai-setup", "--grafana", cfg.Grafana, "--ch-host", chHost(cfg.CH), "--ch-port", chPort(cfg.CH)); err != nil {
		fmt.Fprintf(out, "\n[!] ai-setup error (continuing anyway -- a live recording shouldn't halt): %v\n", err)
	}

	fmt.Fprintln(out)
	stop, err := waitForEnter(reader, out,
		"Once the AI says it's done (dashboard URL + one sentence per panel), press Enter to run the take-QA gate...")
	if err != nil {
		return err
	}
	if stop {
		fmt.Fprintln(out, "\n(stdin closed -- stopping before the take-QA gate)")
		return nil
	}

	fmt.Fprintln(out, "\nrunning: democtl verify ai-dashboard")
	if err := run(ctx, "verify", "ai-dashboard", "--grafana", cfg.Grafana, "--ch", cfg.CH); err != nil {
		fmt.Fprintln(out, "\n[FAIL] ai-build: take-QA gate did not pass.")
		fmt.Fprintln(out, "bad take -- reset with `democtl ai-reset` and re-record.")
		return fmt.Errorf("tour: ai-build: %w", err)
	}
	fmt.Fprintln(out, "\n[PASS] ai-build: take-QA gate passed -- this take is good.")
	return nil
}

const aiQAPreface = `Same MCP session -- the AI still holds the YANG context and its own schema
discoveries from phase 6. Now ask it the rehearsed ad-hoc questions below,
one at a time, in that same chat.

Preface (say once): "Now answer some operational questions. For every
answer, show the SQL you ran and cite the numbers it returned."`

// runAIQAPhase narrates phase 7, prints the rehearsed questions (from
// cfg.PromptsPath), and prints the ground-truth `verify dedup` result for
// Q1 (the outage "prove it" question) so the presenter can confirm the
// AI's answer live, on camera, against real numbers rather than trusting
// it blind.
func runAIQAPhase(ctx context.Context, cfg AIConfig, reader *bufio.Reader, out io.Writer, run runFunc) {
	fmt.Fprint(out, "\n================ Phase 7/7: ai-qa (ask the data) ================\n\n")
	fmt.Fprintln(out, aiQAPreface)
	fmt.Fprintln(out)

	if content, err := os.ReadFile(cfg.PromptsPath); err != nil {
		fmt.Fprintf(out, "[!] could not read %s: %v (read the questions from the repo directly)\n", cfg.PromptsPath, err)
	} else {
		fmt.Fprintln(out, strings.TrimSpace(string(content)))
	}

	fmt.Fprintf(out, "\nGround truth for Q1 (the %s outage window you cut/restored per docs/RUNBOOK.md before this\n", cfg.DOT)
	fmt.Fprintln(out, "session's recording checklist) -- confirm the AI's answer against this live result on camera:")
	fmt.Fprintln(out, "running: democtl verify dedup --dot "+cfg.DOT)
	if err := run(ctx, "verify", "dedup", "--dot", cfg.DOT, "--ch", cfg.CH); err != nil {
		fmt.Fprintf(out, "[!] verify dedup error: %v (state the RUNBOOK-recorded outage window from your own notes instead)\n", err)
	}

	_, _ = waitForEnter(reader, out, "\nPress Enter once the Q&A beat is done...")
}

// chHost/chPort split a ClickHouse HTTP base URL (e.g.
// "http://localhost:8123") into the host/port ai-setup prints in its env
// block, without pulling in net/url just for this -- the URL is always one
// of democtl's own --ch defaults or an explicit override, never untrusted
// input.
func chHost(chURL string) string {
	host, _ := splitHostPort(chURL)
	return host
}

func chPort(chURL string) string {
	_, port := splitHostPort(chURL)
	return port
}

func splitHostPort(chURL string) (host, port string) {
	s := chURL
	if i := strings.Index(s, "://"); i != -1 {
		s = s[i+3:]
	}
	if i := strings.Index(s, "/"); i != -1 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, ":"); i != -1 {
		return s[:i], s[i+1:]
	}
	return s, "8123"
}
