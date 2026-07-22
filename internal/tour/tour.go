// Package tour implements the scripted 5-phase demo tour: for each phase, a
// narration block (what's about to happen, which dashboard(s) to show, what
// the audience will see), an action against the live stack, a settle delay,
// and an assertion against ClickHouse (and the sim's own HTTP surface)
// proving the phase's expected effect actually landed.
//
// One phase table (built by BuildPhases) drives two callers (see
// cmd/democtl's "tour" subcommand and Run below):
//
//   - Paced mode (--paced): the presenter/recording driver. Prints the
//     narration block, waits for Enter, runs the action, prints what to
//     watch for on the dashboard, waits for Enter again. Never runs
//     assertions — the presenter is watching the dashboard live, not a
//     PASS/FAIL log, and the CLI is off-camera so its output stays quiet.
//   - Verify mode (the default, and --verify explicitly): the rehearsal /
//     take-QA driver. Runs every phase unattended — action, sleep the
//     phase's settle duration, run the assertion, print PASS/FAIL — and
//     returns a non-nil error if any phase fails, so a bad take is never
//     mistaken for a clean one.
//
// The AI segment (a later task) is not represented here yet; BuildPhases'
// returned slice is a plain []Phase, so a future phase 6 (or a
// BuildPhases variant) is a pure append, not a rewrite.
package tour

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Vikasa2M/vikasa-demo/internal/verify"
)

// DashboardRef is one Grafana dashboard to point the camera at during a
// phase: its title (for narration) and full URL (to type or click).
type DashboardRef struct {
	Title string
	URL   string
}

// Phase is one step of the scripted tour.
type Phase struct {
	// Name is a short, --only-filterable identifier (e.g. "wan-cut"), also
	// used as the PASS/FAIL label in verify mode. Must be unique within a
	// phase table.
	Name string

	// Narration is what the presenter says before triggering the phase's
	// action: what's about to happen, why it matters, which dashboard(s)
	// to show. Printed as-is in paced mode.
	Narration string

	// Dashboards are the Grafana dashboard(s) to have on screen for this
	// phase, in the order they should be shown.
	Dashboards []DashboardRef

	// WatchFor is what the presenter should point out on the dashboard(s)
	// once the action has landed and had time to visibly register.
	WatchFor string

	// Action performs the phase's effect against the live stack. Nil for
	// phases with no action of their own (phase 1: the stack must simply
	// already be healthy).
	Action func(ctx context.Context) error

	// Settle is how long verify mode waits after Action before running
	// Assert, giving the effect time to propagate through the pipeline
	// (leaf -> regional -> central/dmz -> sink -> ClickHouse) and, for the
	// wan-cut phase, time for the heartbeat gap to clear the staleness
	// threshold. Paced mode ignores Settle — the presenter paces
	// themselves by pressing Enter.
	Settle time.Duration

	// Assert proves the phase's expected outcome against the live stack.
	// Always non-nil: verify mode has nothing to check otherwise. Paced
	// mode never calls it.
	Assert func(ctx context.Context) error
}

// RunOptions configures Run's driving mode.
type RunOptions struct {
	// Paced selects presenter/recording mode: narrate, wait for Enter,
	// act, describe what to watch for, wait for Enter again. False (the
	// default) selects verify mode: act, sleep Settle, assert, print
	// PASS/FAIL, keep going.
	Paced bool

	// Settle, present (nonzero), overrides every phase's own Settle in
	// verify mode — for rehearsing a single phase (with FilterPhases)
	// faster than its production settle time allows. Ignored in paced
	// mode.
	Settle time.Duration

	In  io.Reader // stdin for paced mode's Enter gate; defaults to os.Stdin
	Out io.Writer // narration/status output; defaults to os.Stdout
}

// Run drives phases through either paced or verify mode (see RunOptions).
// In verify mode it returns a non-nil error naming every phase whose action
// or assertion failed; the caller should exit nonzero on that error so a
// bad take is never mistaken for a clean one. Paced mode always returns nil
// (short of a context cancellation) — it has no PASS/FAIL notion.
func Run(ctx context.Context, phases []Phase, opts RunOptions) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	in := opts.In
	if in == nil {
		in = os.Stdin
	}
	reader := bufio.NewReader(in)

	if opts.Paced {
		return runPaced(ctx, phases, reader, out)
	}
	return runVerify(ctx, phases, opts.Settle, out)
}

func runPaced(ctx context.Context, phases []Phase, reader *bufio.Reader, out io.Writer) error {
	for i, p := range phases {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprintf(out, "\n================ Phase %d/%d: %s ================\n\n", i+1, len(phases), p.Name)
		for _, d := range p.Dashboards {
			fmt.Fprintf(out, "DASHBOARD: %s\n  %s\n", d.Title, d.URL)
		}
		fmt.Fprintln(out)
		fmt.Fprintln(out, p.Narration)
		fmt.Fprintln(out)
		stop, err := waitForEnter(reader, out, "Press Enter to trigger this phase...")
		if err != nil {
			return err
		}
		if stop {
			fmt.Fprintln(out, "\n(stdin closed before this phase was triggered — stopping the tour; no further actions will run)")
			return nil
		}

		if p.Action != nil {
			if err := p.Action(ctx); err != nil {
				fmt.Fprintf(out, "\n[!] action error (continuing anyway — a live recording shouldn't halt): %v\n", err)
			}
		}

		fmt.Fprintf(out, "\nWATCH FOR: %s\n\n", p.WatchFor)
		stop, err = waitForEnter(reader, out, "Press Enter to continue to the next phase...")
		if err != nil {
			return err
		}
		if stop {
			fmt.Fprintln(out, "\n(stdin closed — stopping the tour; no further actions will run)")
			return nil
		}
	}
	fmt.Fprintln(out, "\ntour complete.")
	return nil
}

// waitForEnter blocks until reader yields a line (a real presenter pressing
// Enter) or is exhausted. stop=true means stdin hit EOF: the caller MUST
// NOT proceed to the phase's action — an exhausted/closed stdin is treated
// as "stop the tour", never as an implicit Enter press, so a paced run can
// never silently blow through its remaining live actions (cut, restore,
// fault/incident injection) unattended just because stdin closed.
func waitForEnter(reader *bufio.Reader, out io.Writer, prompt string) (stop bool, err error) {
	fmt.Fprint(out, prompt)
	if _, err := reader.ReadString('\n'); err != nil {
		if err == io.EOF {
			return true, nil
		}
		return false, fmt.Errorf("tour: read stdin: %w", err)
	}
	return false, nil
}

func runVerify(ctx context.Context, phases []Phase, settleOverride time.Duration, out io.Writer) error {
	var failed []string
	for i, p := range phases {
		fmt.Fprintf(out, "\n=== Phase %d/%d: %s ===\n", i+1, len(phases), p.Name)
		if p.Action != nil {
			if err := p.Action(ctx); err != nil {
				fmt.Fprintf(out, "[FAIL] %s: action error: %v\n", p.Name, err)
				failed = append(failed, p.Name)
				continue
			}
		}

		settle := p.Settle
		if settleOverride > 0 {
			settle = settleOverride
		}
		if settle > 0 {
			fmt.Fprintf(out, "settling %s...\n", settle)
			select {
			case <-time.After(settle):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		if p.Assert == nil {
			fmt.Fprintf(out, "[SKIP] %s: no assertion defined\n", p.Name)
			continue
		}
		if err := p.Assert(ctx); err != nil {
			fmt.Fprintf(out, "[FAIL] %s: %v\n", p.Name, err)
			failed = append(failed, p.Name)
			continue
		}
		fmt.Fprintf(out, "[PASS] %s\n", p.Name)
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d/%d phase(s) failed: %s", len(failed), len(phases), strings.Join(failed, ", "))
	}
	return nil
}

// FilterPhases returns the subset of phases named in only, preserving
// phases' declared order (not the order names were given in only). A
// phase's assertion may depend on a still-live effect from an earlier phase
// run in a PREVIOUS invocation (e.g. restore expects a prior wan-cut), so
// rehearsing one phase at a time across several `democtl tour --only=...`
// invocations is an intended use — declared order just governs relative
// ordering when several names are given at once.
func FilterPhases(phases []Phase, only []string) ([]Phase, error) {
	want := map[string]bool{}
	for _, name := range only {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		want[name] = true
	}
	if len(want) == 0 {
		return phases, nil
	}
	var out []Phase
	for _, p := range phases {
		if want[p.Name] {
			out = append(out, p)
			delete(want, p.Name)
		}
	}
	if len(want) > 0 {
		unknown := make([]string, 0, len(want))
		for name := range want {
			unknown = append(unknown, name)
		}
		return nil, fmt.Errorf("tour: unknown phase name(s): %s", strings.Join(unknown, ", "))
	}
	return out, nil
}

// queryScalar runs query against ch and parses the response as a single
// integer — the shape every assertion in this package's queries returns
// (each query builder in queries.go ends in `FORMAT TSV` around one
// count()/dateDiff() column).
func queryScalar(ctx context.Context, ch, query string) (int64, error) {
	out, err := verify.Query(ctx, ch, query)
	if err != nil {
		return 0, err
	}
	return verify.ParseScalarInt(out)
}

// firstFailure returns the first failing CheckResult in results, or nil if
// every check passed.
func firstFailure(results []verify.CheckResult) *verify.CheckResult {
	for i := range results {
		if !results[i].Pass {
			return &results[i]
		}
	}
	return nil
}

// dashboardURL builds a Grafana dashboard link from its uid. Grafana
// resolves /d/<uid> regardless of the (cosmetic) slug segment, so no slug
// is needed here.
func dashboardURL(base, uid string) string {
	return strings.TrimRight(base, "/") + "/d/" + uid
}
