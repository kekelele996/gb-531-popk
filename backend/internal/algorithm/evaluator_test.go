package algorithm

import (
	"strings"
	"testing"
	"time"

	"hazop-safeguard-coverage/backend/internal/model"
)

func TestEvaluatorIsDeterministicAndDeduplicatesIndependenceKeys(t *testing.T) {
	t.Parallel()
	reference := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	valid := reference.AddDate(0, 0, -10)
	expired := reference.AddDate(0, 0, -400)
	node := model.ProcessNode{ID: 7, NodeCode: "R-7", Name: "Reactor"}
	scenario := model.DeviationScenario{
		ID: 9, Guideword: "more", Parameter: "temperature",
		Cause: "cooling loss; runaway reaction", Consequence: "overpressure; release",
		Likelihood: 4, Severity: 5, ScenarioState: "analyzed", Version: 2,
	}
	safeguards := []model.Safeguard{
		{ID: 3, IndependenceKey: "SIS-A", Effectiveness: 0.4, TestIntervalDays: 365, LastVerifiedAt: &valid, LifecycleState: "active"},
		{ID: 2, IndependenceKey: "SIS-A", Effectiveness: 0.8, TestIntervalDays: 365, LastVerifiedAt: &valid, LifecycleState: "active"},
		{ID: 4, IndependenceKey: "PSV-B", Effectiveness: 0.9, TestIntervalDays: 365, LastVerifiedAt: &expired, LifecycleState: "expired"},
	}
	evaluator := NewEvaluator()
	first, err := evaluator.Evaluate(NewSnapshot(node, scenario, safeguards, reference))
	if err != nil {
		t.Fatalf("first evaluation failed: %v", err)
	}
	second, err := evaluator.Evaluate(NewSnapshot(node, scenario, safeguards, reference))
	if err != nil {
		t.Fatalf("second evaluation failed: %v", err)
	}
	if first.InputHash != second.InputHash || first.ExplanationJSON != second.ExplanationJSON {
		t.Fatal("same frozen input produced different output")
	}
	if first.CoverageScore != 80 {
		t.Fatalf("coverage score = %v, want 80", first.CoverageScore)
	}
	if len(first.Explanation.Deduplicated) != 1 || first.Explanation.Deduplicated[0].KeptID != 2 {
		t.Fatalf("unexpected deduplication: %#v", first.Explanation.Deduplicated)
	}
	passed, replayed, err := evaluator.Replay(first.SnapshotJSON, first.InputHash, first.CoverageScore)
	if err != nil || !passed || replayed.CoverageScore != first.CoverageScore {
		t.Fatalf("replay failed: passed=%t score=%v err=%v", passed, replayed.CoverageScore, err)
	}
}

func TestEvaluatorDetectsUnprotectedPaths(t *testing.T) {
	t.Parallel()
	reference := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	node := model.ProcessNode{ID: 1, NodeCode: "V-1", Name: "Vessel"}
	scenario := model.DeviationScenario{
		ID: 1, Guideword: "more", Parameter: "pressure",
		Cause: "blocked outlet", Consequence: "rupture", Likelihood: 3, Severity: 5,
		ScenarioState: "draft", Version: 1,
	}
	result, err := NewEvaluator().Evaluate(NewSnapshot(node, scenario, nil, reference))
	if err != nil {
		t.Fatalf("evaluation failed: %v", err)
	}
	if result.CoverageScore != 0 || len(result.Explanation.Paths) != 1 || result.Explanation.Paths[0].Covered {
		t.Fatalf("expected one unprotected path, got %#v", result.Explanation.Paths)
	}
}

func TestEvaluatorExcludesSuspendedSafeguard(t *testing.T) {
	t.Parallel()
	reference := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	valid := reference.AddDate(0, 0, -10)
	node := model.ProcessNode{ID: 5, NodeCode: "R-5", Name: "Reactor"}
	scenario := model.DeviationScenario{
		ID: 6, Guideword: "more", Parameter: "temperature",
		Cause: "cooling loss", Consequence: "overpressure", Likelihood: 4, Severity: 5,
		ScenarioState: "analyzed", Version: 1,
	}
	safeguards := []model.Safeguard{
		{
			ID: 8, IndependenceKey: "SIS-C", Effectiveness: 0.9, TestIntervalDays: 365,
			LastVerifiedAt: &valid, LifecycleState: "suspended", SuspensionReason: "field disassembly",
		},
	}
	result, err := NewEvaluator().Evaluate(NewSnapshot(node, scenario, safeguards, reference))
	if err != nil {
		t.Fatalf("evaluation failed: %v", err)
	}
	if result.CoverageScore != 0 || len(result.UncoveredJSON) == 0 {
		t.Fatalf("suspended safeguard must not contribute, score = %v", result.CoverageScore)
	}
	explained := false
	for _, step := range result.Explanation.ScoreSteps {
		if step.Rule == "eligibility-filter" && strings.Contains(step.Explanation, "suspended") && strings.Contains(step.Explanation, "field disassembly") {
			explained = true
		}
	}
	if !explained {
		t.Fatalf("score steps should explain the suspension, got %#v", result.Explanation.ScoreSteps)
	}
	if !strings.Contains(result.SnapshotJSON, `"suspension_reason":"field disassembly"`) {
		t.Fatal("snapshot should carry the suspension reason for a suspended safeguard")
	}
	passed, _, err := NewEvaluator().Replay(result.SnapshotJSON, result.InputHash, result.CoverageScore)
	if err != nil || !passed {
		t.Fatalf("suspended snapshot replay failed: passed=%t err=%v", passed, err)
	}
}

func TestSnapshotOmitsSuspensionReasonWhenNotSuspended(t *testing.T) {
	t.Parallel()
	reference := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	valid := reference.AddDate(0, 0, -10)
	node := model.ProcessNode{ID: 5, NodeCode: "R-5", Name: "Reactor"}
	scenario := model.DeviationScenario{
		ID: 6, Guideword: "more", Parameter: "temperature",
		Cause: "cooling loss", Consequence: "overpressure", Likelihood: 4, Severity: 5,
		ScenarioState: "analyzed", Version: 1,
	}
	safeguards := []model.Safeguard{
		{ID: 8, IndependenceKey: "SIS-C", Effectiveness: 0.9, TestIntervalDays: 365, LastVerifiedAt: &valid, LifecycleState: "active"},
	}
	result, err := NewEvaluator().Evaluate(NewSnapshot(node, scenario, safeguards, reference))
	if err != nil {
		t.Fatalf("evaluation failed: %v", err)
	}
	if strings.Contains(result.SnapshotJSON, "suspension_reason") {
		t.Fatal("snapshot of a non-suspended safeguard must omit suspension_reason so historical input hashes stay stable")
	}
}
