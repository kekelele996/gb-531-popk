package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"hazop-safeguard-coverage/backend/internal/algorithm"
	"hazop-safeguard-coverage/backend/internal/dto"
	"hazop-safeguard-coverage/backend/internal/model"
	"hazop-safeguard-coverage/backend/internal/repository"
	"hazop-safeguard-coverage/backend/internal/util"
)

func TestSafeguardSuspendAndResumeLifecycle(t *testing.T) {
	db := testDB(t)
	safeguardRepo := repository.NewSafeguardRepository(db)
	scenarioRepo := repository.NewDeviationScenarioRepository(db)
	nodeRepo := repository.NewProcessNodeRepository(db)
	auditRepo := repository.NewAuditRepository(db)
	ctx := context.Background()
	node := model.ProcessNode{
		NodeCode: "T-201", Name: "Suspend Node", UnitName: "Test Unit", Medium: "solvent",
		DesignPressure: 1, DesignTemperature: 100, OwnerTeam: "test", Status: "active",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := nodeRepo.Create(ctx, &node); err != nil {
		t.Fatalf("create node: %v", err)
	}
	scenario := model.DeviationScenario{
		ProcessNodeID: node.ID, Guideword: "more", Parameter: "pressure",
		Cause: "blocked outlet", Consequence: "overpressure", Likelihood: 3, Severity: 4,
		ScenarioState: "analyzed", Version: 1, CreatedBy: 10, CreatedByName: "engineer",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := scenarioRepo.Create(ctx, &scenario); err != nil {
		t.Fatalf("create scenario: %v", err)
	}
	service := NewSafeguardService(safeguardRepo, scenarioRepo, auditRepo)
	engineer := util.Actor{UserID: 10, Username: "engineer", Role: "process_engineer", RequestID: "req-suspend"}
	reviewer := util.Actor{UserID: 20, Username: "reviewer", Role: "safety_reviewer", RequestID: "req-resume"}
	verified := time.Now().UTC().AddDate(0, 0, -10)
	created, err := service.Create(ctx, dto.CreateSafeguardRequest{
		Name: "PSV on test vessel", SafeguardType: "relief", TargetScenarioID: scenario.ID,
		IndependenceKey: "PSV-T201-01", Effectiveness: 0.9, TestIntervalDays: 180,
		LastVerifiedAt: &verified, EvidenceNote: "initial proof-test certificate",
	}, engineer)
	if err != nil || created.LifecycleState != "active" {
		t.Fatalf("create safeguard: state=%s err=%v", created.LifecycleState, err)
	}
	past := time.Now().UTC().Add(-time.Hour)
	_, err = service.Suspend(ctx, created.ID, dto.SuspendSafeguardRequest{
		Reason: "field disassembly", CompensatingMeasures: "manual watch per shift", PlannedRestoreAt: past,
	}, engineer)
	var appErr *util.AppError
	if !errors.As(err, &appErr) || appErr.Status != 422 {
		t.Fatalf("past planned restore should return 422, got %v", err)
	}
	planned := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)
	suspended, err := service.Suspend(ctx, created.ID, dto.SuspendSafeguardRequest{
		Reason: "现场拆检安全阀", CompensatingMeasures: "每班两次人工巡检并监视压力", PlannedRestoreAt: planned,
	}, engineer)
	if err != nil {
		t.Fatalf("suspend safeguard: %v", err)
	}
	if suspended.LifecycleState != "suspended" {
		t.Fatalf("lifecycle = %s, want suspended", suspended.LifecycleState)
	}
	if suspended.SuspensionReason != "现场拆检安全阀" || suspended.CompensatingMeasures == "" {
		t.Fatalf("suspension fields not persisted: %#v", suspended)
	}
	if suspended.SuspendedAt == nil || suspended.SuspendedBy == nil || *suspended.SuspendedBy != engineer.UserID {
		t.Fatalf("suspension actor not recorded: %#v", suspended)
	}
	if suspended.PlannedRestoreAt == nil || suspended.RestoreOverdue {
		t.Fatalf("planned restore = %v overdue=%t, want future date and not overdue", suspended.PlannedRestoreAt, suspended.RestoreOverdue)
	}
	_, err = service.Suspend(ctx, created.ID, dto.SuspendSafeguardRequest{
		Reason: "duplicate suspension", CompensatingMeasures: "none", PlannedRestoreAt: planned,
	}, engineer)
	if !errors.As(err, &appErr) || appErr.Status != 409 || appErr.Code != util.CodeStateTransition {
		t.Fatalf("re-suspend should conflict, got %v", err)
	}
	_, err = service.Resume(ctx, created.ID, dto.ResumeSafeguardRequest{
		VerifiedAt: time.Now().UTC(), EvidenceNote: "   ",
	}, reviewer)
	if !errors.As(err, &appErr) || appErr.Status != 422 {
		t.Fatalf("resume without evidence should return 422, got %v", err)
	}
	stillSuspended, getErr := service.Get(ctx, created.ID)
	if getErr != nil || stillSuspended.LifecycleState != "suspended" {
		t.Fatalf("safeguard must stay suspended without evidence: state=%s err=%v", stillSuspended.LifecycleState, getErr)
	}
	stale := time.Now().UTC().AddDate(0, 0, -400)
	_, err = service.Resume(ctx, created.ID, dto.ResumeSafeguardRequest{
		VerifiedAt: stale, EvidenceNote: "outdated proof-test report",
	}, reviewer)
	if !errors.As(err, &appErr) || appErr.Status != 422 {
		t.Fatalf("expired verification should return 422, got %v", err)
	}
	verifiedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	resumed, err := service.Resume(ctx, created.ID, dto.ResumeSafeguardRequest{
		VerifiedAt: verifiedAt, EvidenceNote: "回装后功能试验报告 FT-2026-091",
	}, reviewer)
	if err != nil {
		t.Fatalf("resume safeguard: %v", err)
	}
	if resumed.LifecycleState != "active" {
		t.Fatalf("lifecycle = %s, want active", resumed.LifecycleState)
	}
	if resumed.LastVerifiedAt == nil {
		t.Fatal("last_verified_at must follow the reviewer verification time")
	}
	if delta := resumed.LastVerifiedAt.Sub(verifiedAt); delta > 2*time.Second || delta < -2*time.Second {
		t.Fatalf("last_verified_at = %v, want %v", resumed.LastVerifiedAt, verifiedAt)
	}
	if resumed.ResumedAt == nil || resumed.ResumedBy == nil || *resumed.ResumedBy != reviewer.UserID {
		t.Fatalf("resume actor not recorded: %#v", resumed)
	}
	if resumed.EvidenceNote != "回装后功能试验报告 FT-2026-091" {
		t.Fatalf("evidence note = %q", resumed.EvidenceNote)
	}
	if resumed.SuspensionReason == "" || suspended.PlannedRestoreAt == nil {
		t.Fatal("suspension history should stay visible on the ledger after resume")
	}
	_, err = service.Resume(ctx, created.ID, dto.ResumeSafeguardRequest{
		VerifiedAt: verifiedAt, EvidenceNote: "second resume attempt",
	}, reviewer)
	if !errors.As(err, &appErr) || appErr.Status != 409 {
		t.Fatalf("resume of an active safeguard should conflict, got %v", err)
	}
}

func TestSuspendedSafeguardExcludedFromNewEvaluations(t *testing.T) {
	db := testDB(t)
	safeguardRepo := repository.NewSafeguardRepository(db)
	scenarioRepo := repository.NewDeviationScenarioRepository(db)
	nodeRepo := repository.NewProcessNodeRepository(db)
	evaluationRepo := repository.NewCoverageEvaluationRepository(db)
	auditRepo := repository.NewAuditRepository(db)
	ctx := context.Background()
	node := model.ProcessNode{
		NodeCode: "T-202", Name: "Coverage Node", UnitName: "Test Unit", Medium: "solvent",
		DesignPressure: 1, DesignTemperature: 100, OwnerTeam: "test", Status: "active",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := nodeRepo.Create(ctx, &node); err != nil {
		t.Fatalf("create node: %v", err)
	}
	scenario := model.DeviationScenario{
		ProcessNodeID: node.ID, Guideword: "more", Parameter: "temperature",
		Cause: "cooling loss", Consequence: "runaway reaction", Likelihood: 4, Severity: 5,
		ScenarioState: "analyzed", Version: 1, CreatedBy: 10, CreatedByName: "engineer",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := scenarioRepo.Create(ctx, &scenario); err != nil {
		t.Fatalf("create scenario: %v", err)
	}
	safeguardService := NewSafeguardService(safeguardRepo, scenarioRepo, auditRepo)
	evaluationService := NewCoverageEvaluationService(
		evaluationRepo, scenarioRepo, nodeRepo, safeguardRepo, auditRepo, algorithm.NewEvaluator(),
	)
	engineer := util.Actor{UserID: 10, Username: "engineer", Role: "process_engineer", RequestID: "req-run"}
	reviewer := util.Actor{UserID: 20, Username: "reviewer", Role: "safety_reviewer", RequestID: "req-review"}
	verified := time.Now().UTC().AddDate(0, 0, -10)
	safeguard, err := safeguardService.Create(ctx, dto.CreateSafeguardRequest{
		Name: "High temperature SIS trip", SafeguardType: "interlock", TargetScenarioID: scenario.ID,
		IndependenceKey: "SIS-T202-TEMP", Effectiveness: 0.8, TestIntervalDays: 365,
		LastVerifiedAt: &verified, EvidenceNote: "proof-test certificate",
	}, engineer)
	if err != nil {
		t.Fatalf("create safeguard: %v", err)
	}
	first, reused, err := evaluationService.Run(ctx, dto.RunCoverageEvaluationRequest{ScenarioID: scenario.ID}, "idem-key-000001", engineer)
	if err != nil || reused {
		t.Fatalf("first run: reused=%t err=%v", reused, err)
	}
	if first.CoverageScore != 80 || first.EvaluationState != "completed" {
		t.Fatalf("first evaluation score = %v state=%s, want 80 completed", first.CoverageScore, first.EvaluationState)
	}
	if _, err := safeguardService.Suspend(ctx, safeguard.ID, dto.SuspendSafeguardRequest{
		Reason: "现场拆检", CompensatingMeasures: "临时人工监控", PlannedRestoreAt: time.Now().UTC().Add(48 * time.Hour),
	}, engineer); err != nil {
		t.Fatalf("suspend safeguard: %v", err)
	}
	second, reused, err := evaluationService.Run(ctx, dto.RunCoverageEvaluationRequest{ScenarioID: scenario.ID}, "idem-key-000002", engineer)
	if err != nil || reused {
		t.Fatalf("second run: reused=%t err=%v", reused, err)
	}
	if second.CoverageScore != 0 || len(second.UncoveredPaths) != 1 {
		t.Fatalf("suspended safeguard must not count: score=%v uncovered=%d", second.CoverageScore, len(second.UncoveredPaths))
	}
	suspensionExplained := false
	for _, step := range second.Explanation.ScoreSteps {
		if strings.Contains(step.Explanation, "suspended") {
			suspensionExplained = true
		}
	}
	if !suspensionExplained {
		t.Fatal("score steps should explain the suspended safeguard exclusion")
	}
	reloaded, err := evaluationService.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("reload first evaluation: %v", err)
	}
	if reloaded.CoverageScore != 80 || reloaded.InputHash != first.InputHash {
		t.Fatalf("historical evaluation changed: score=%v hash=%s", reloaded.CoverageScore, reloaded.InputHash)
	}
	if !strings.Contains(string(reloaded.InputSnapshot), `"lifecycle_state":"active"`) {
		t.Fatal("historical snapshot must keep the safeguard active state frozen")
	}
	if _, err := evaluationService.Replay(ctx, first.ID, reviewer); err != nil {
		t.Fatalf("historical evaluation must still replay after suspension: %v", err)
	}
	resumed, err := safeguardService.Resume(ctx, safeguard.ID, dto.ResumeSafeguardRequest{
		VerifiedAt: time.Now().UTC().Add(-time.Hour), EvidenceNote: "回装验证报告",
	}, reviewer)
	if err != nil || resumed.LifecycleState != "active" {
		t.Fatalf("resume safeguard: state=%s err=%v", resumed.LifecycleState, err)
	}
	third, reused, err := evaluationService.Run(ctx, dto.RunCoverageEvaluationRequest{ScenarioID: scenario.ID}, "idem-key-000003", engineer)
	if err != nil || reused {
		t.Fatalf("third run: reused=%t err=%v", reused, err)
	}
	if third.CoverageScore != 80 {
		t.Fatalf("resumed safeguard must count again: score=%v, want 80", third.CoverageScore)
	}
}
