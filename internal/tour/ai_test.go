package tour

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunAIPhasesBuildOnlyGatesOnEnterAndRunsVerify(t *testing.T) {
	var calls [][]string
	fakeRun := func(_ context.Context, args ...string) error {
		calls = append(calls, args)
		return nil
	}

	in := bytes.NewBufferString("\n") // one Enter press: confirm the AI is done
	var out bytes.Buffer
	only := map[string]bool{PhaseAIBuild: true}

	err := runAIPhases(context.Background(), AIConfig{}, only, bufio.NewReader(in), &out, fakeRun)
	if err != nil {
		t.Fatalf("runAIPhases: unexpected error: %v", err)
	}

	if len(calls) != 2 {
		t.Fatalf("got %d external calls, want 2 (ai-setup, then verify ai-dashboard): %+v", len(calls), calls)
	}
	if calls[0][0] != "ai-setup" {
		t.Errorf("calls[0] = %v, want it to start with ai-setup", calls[0])
	}
	if calls[1][0] != "verify" || calls[1][1] != "ai-dashboard" {
		t.Errorf("calls[1] = %v, want it to be verify ai-dashboard", calls[1])
	}

	printed := out.String()
	if !strings.Contains(printed, "Phase 6/7: ai-build") {
		t.Errorf("output missing phase 6 header:\n%s", printed)
	}
	if !strings.Contains(printed, "[PASS] ai-build") {
		t.Errorf("output missing the PASS line for a successful take-QA gate:\n%s", printed)
	}
	if strings.Contains(printed, "Phase 7/7") {
		t.Errorf("only={ai-build} must not run phase 7:\n%s", printed)
	}
}

func TestRunAIPhasesBuildReportsBadTakeOnVerifyFailure(t *testing.T) {
	fakeRun := func(_ context.Context, args ...string) error {
		if args[0] == "verify" {
			return errFake
		}
		return nil
	}

	in := bytes.NewBufferString("\n")
	var out bytes.Buffer
	only := map[string]bool{PhaseAIBuild: true}

	err := runAIPhases(context.Background(), AIConfig{}, only, bufio.NewReader(in), &out, fakeRun)
	if err == nil {
		t.Fatal("runAIPhases: want an error when the take-QA gate fails, got nil")
	}

	printed := out.String()
	if !strings.Contains(printed, "bad take") || !strings.Contains(printed, "democtl ai-reset") {
		t.Errorf("output missing the bad-take/ai-reset guidance:\n%s", printed)
	}
	if !strings.Contains(printed, "[FAIL] ai-build") {
		t.Errorf("output missing the FAIL line:\n%s", printed)
	}
}

func TestRunAIPhasesBuildStdinClosedStopsBeforeVerify(t *testing.T) {
	var calls [][]string
	fakeRun := func(_ context.Context, args ...string) error {
		calls = append(calls, args)
		return nil
	}

	in := bytes.NewBufferString("") // EOF immediately -- no Enter press
	var out bytes.Buffer
	only := map[string]bool{PhaseAIBuild: true}

	err := runAIPhases(context.Background(), AIConfig{}, only, bufio.NewReader(in), &out, fakeRun)
	if err != nil {
		t.Fatalf("runAIPhases: unexpected error: %v", err)
	}
	if len(calls) != 1 || calls[0][0] != "ai-setup" {
		t.Errorf("got calls=%v, want exactly [ai-setup] (verify ai-dashboard must not run once stdin is closed before the gate)", calls)
	}
	if !strings.Contains(out.String(), "stdin closed") {
		t.Errorf("output missing the stdin-closed notice:\n%s", out.String())
	}
}

func TestRunAIPhasesQAOnlyPrintsQuestionsAndGroundTruth(t *testing.T) {
	var calls [][]string
	fakeRun := func(_ context.Context, args ...string) error {
		calls = append(calls, args)
		return nil
	}

	in := bytes.NewBufferString("\n")
	var out bytes.Buffer
	only := map[string]bool{PhaseAIQA: true}
	cfg := AIConfig{DOT: "gdot", PromptsPath: "does/not/exist.md"}

	err := runAIPhases(context.Background(), cfg, only, bufio.NewReader(in), &out, fakeRun)
	if err != nil {
		t.Fatalf("runAIPhases: unexpected error: %v", err)
	}
	if len(calls) != 1 || calls[0][0] != "verify" || calls[0][1] != "dedup" {
		t.Fatalf("got calls=%v, want exactly one verify dedup call", calls)
	}
	// --dot must be threaded through to the ground-truth dedup check.
	found := false
	for i, a := range calls[0] {
		if a == "--dot" && i+1 < len(calls[0]) && calls[0][i+1] == "gdot" {
			found = true
		}
	}
	if !found {
		t.Errorf("verify dedup call %v missing --dot gdot", calls[0])
	}

	printed := out.String()
	if !strings.Contains(printed, "Phase 7/7: ai-qa") {
		t.Errorf("output missing phase 7 header:\n%s", printed)
	}
	if !strings.Contains(printed, "could not read") {
		t.Errorf("output should note the missing prompts file rather than silently skip it:\n%s", printed)
	}
	if strings.Contains(printed, "Phase 6/7") {
		t.Errorf("only={ai-qa} must not run phase 6:\n%s", printed)
	}
}

func TestRunAIPhasesQAPrintsRealQuestionsFile(t *testing.T) {
	fakeRun := func(context.Context, ...string) error { return nil }
	in := bytes.NewBufferString("\n")
	var out bytes.Buffer
	cfg := AIConfig{DOT: "gdot", PromptsPath: "../../demo/ai/prompts/questions.md"}

	err := runAIPhases(context.Background(), cfg, map[string]bool{PhaseAIQA: true}, bufio.NewReader(in), &out, fakeRun)
	if err != nil {
		t.Fatalf("runAIPhases: unexpected error: %v", err)
	}
	printed := out.String()
	for _, want := range []string{"cab-i85-001", "MAX_OUT", "lidar"} {
		if !strings.Contains(printed, want) {
			t.Errorf("output missing %q from questions.md; got:\n%s", want, printed)
		}
	}
}

func TestRunAIPhasesDefaultRunsBothInOrder(t *testing.T) {
	var calls [][]string
	fakeRun := func(_ context.Context, args ...string) error {
		calls = append(calls, args)
		return nil
	}
	in := bytes.NewBufferString("\n\n")
	var out bytes.Buffer

	err := runAIPhases(context.Background(), AIConfig{PromptsPath: "does/not/exist.md"}, nil, bufio.NewReader(in), &out, fakeRun)
	if err != nil {
		t.Fatalf("runAIPhases: unexpected error: %v", err)
	}
	printed := out.String()
	buildIdx := strings.Index(printed, "Phase 6/7")
	qaIdx := strings.Index(printed, "Phase 7/7")
	if buildIdx == -1 || qaIdx == -1 || buildIdx > qaIdx {
		t.Errorf("want phase 6 printed before phase 7 when only is nil; got:\n%s", printed)
	}
	if len(calls) != 3 {
		t.Fatalf("got %d external calls, want 3 (ai-setup, verify ai-dashboard, verify dedup): %+v", len(calls), calls)
	}
}

func TestSplitHostPort(t *testing.T) {
	cases := []struct {
		url, host, port string
	}{
		{"http://localhost:8123", "localhost", "8123"},
		{"http://localhost:8123/", "localhost", "8123"},
		{"https://ch.example.com:9440", "ch.example.com", "9440"},
		{"localhost:8123", "localhost", "8123"},
	}
	for _, tc := range cases {
		host, port := splitHostPort(tc.url)
		if host != tc.host || port != tc.port {
			t.Errorf("splitHostPort(%q) = (%q, %q), want (%q, %q)", tc.url, host, port, tc.host, tc.port)
		}
	}
}
