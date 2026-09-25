package service

import (
	"context"
	"errors"
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
	auditRepo := repository.NewAuditRepository(db)
	nodeRepo := repository.NewProcessNodeRepository(db)
	evaluationRepo := repository.NewCoverageEvaluationRepository(db)

	node := model.ProcessNode{
		NodeCode: "T-201", Name: "Suspend Test Node", UnitName: "Test Unit", Medium: "solvent",
		DesignPressure: 1.5, DesignTemperature: 120, OwnerTeam: "test", Status: "active",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := nodeRepo.Create(context.Background(), &node); err != nil {
		t.Fatalf("create node: %v", err)
	}
	scenario := model.DeviationScenario{
		ProcessNodeID: node.ID, Guideword: "more", Parameter: "pressure",
		Cause: "outlet blocked", Consequence: "vessel overpressure",
		Likelihood: 3, Severity: 4, ScenarioState: "analyzed", Version: 1,
		CreatedBy: 10, CreatedByName: "engineer",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := scenarioRepo.Create(context.Background(), &scenario); err != nil {
		t.Fatalf("create scenario: %v", err)
	}

	safeguardService := NewSafeguardService(safeguardRepo, scenarioRepo, auditRepo)
	coverageService := NewCoverageEvaluationService(evaluationRepo, scenarioRepo, nodeRepo, safeguardRepo, auditRepo, algorithm.NewEvaluator())
	engineer := util.Actor{UserID: 10, Username: "engineer", Role: "process_engineer", RequestID: "req-engineer"}
	reviewer := util.Actor{UserID: 20, Username: "reviewer", Role: "safety_reviewer", RequestID: "req-reviewer"}

	verified := time.Now().UTC().AddDate(0, 0, -10).Truncate(time.Second)
	created, err := safeguardService.Create(context.Background(), dto.CreateSafeguardRequest{
		Name: "Pressure relief valve", SafeguardType: "relief", TargetScenarioID: scenario.ID,
		IndependenceKey: "PSV-T201-01", Effectiveness: 0.8, TestIntervalDays: 365,
		LastVerifiedAt: &verified, EvidenceNote: "bench test certificate",
	}, engineer)
	if err != nil {
		t.Fatalf("create safeguard: %v", err)
	}
	if created.LifecycleState != "active" {
		t.Fatalf("expected active safeguard, got %q", created.LifecycleState)
	}

	before, _, err := coverageService.Run(context.Background(), dto.RunCoverageEvaluationRequest{ScenarioID: scenario.ID}, "suspend-flow-0001", engineer)
	if err != nil {
		t.Fatalf("run baseline evaluation: %v", err)
	}
	if before.CoverageScore != 80 {
		t.Fatalf("baseline coverage score should be 80, got %v", before.CoverageScore)
	}

	var appErr *util.AppError
	_, err = safeguardService.Suspend(context.Background(), created.ID, dto.SuspendSafeguardRequest{
		Reason: "field disassembly inspection", AlternativeMeasure: "  ",
		PlannedRestoreAt: time.Now().UTC().AddDate(0, 0, 5),
	}, engineer)
	if !errors.As(err, &appErr) || appErr.Status != 422 {
		t.Fatalf("suspend without alternative measure should return 422, got %v", err)
	}
	_, err = safeguardService.Suspend(context.Background(), created.ID, dto.SuspendSafeguardRequest{
		Reason: "field disassembly inspection", AlternativeMeasure: "hourly operator rounds",
		PlannedRestoreAt: time.Now().UTC().AddDate(0, 0, -1),
	}, engineer)
	if !errors.As(err, &appErr) || appErr.Status != 422 {
		t.Fatalf("suspend with past planned restore date should return 422, got %v", err)
	}

	plannedRestore := time.Now().UTC().AddDate(0, 0, 7).Truncate(time.Second)
	suspended, err := safeguardService.Suspend(context.Background(), created.ID, dto.SuspendSafeguardRequest{
		Reason: "field disassembly inspection", AlternativeMeasure: "hourly operator rounds with signed log",
		PlannedRestoreAt: plannedRestore,
	}, engineer)
	if err != nil {
		t.Fatalf("suspend safeguard: %v", err)
	}
	if suspended.LifecycleState != "suspended" {
		t.Fatalf("expected suspended state, got %q", suspended.LifecycleState)
	}
	if suspended.SuspensionReason != "field disassembly inspection" ||
		suspended.AlternativeMeasure != "hourly operator rounds with signed log" ||
		suspended.PlannedRestoreAt == nil || !suspended.PlannedRestoreAt.Equal(plannedRestore) ||
		suspended.SuspendedBy == nil || *suspended.SuspendedBy != engineer.UserID {
		t.Fatalf("suspension ledger fields incomplete: %#v", suspended)
	}

	during, _, err := coverageService.Run(context.Background(), dto.RunCoverageEvaluationRequest{ScenarioID: scenario.ID}, "suspend-flow-0002", engineer)
	if err != nil {
		t.Fatalf("run evaluation during suspension: %v", err)
	}
	if during.CoverageScore != 0 {
		t.Fatalf("suspended safeguard must not count; score should be 0, got %v", during.CoverageScore)
	}
	if len(during.UncoveredPaths) == 0 {
		t.Fatalf("suspension should expose uncovered paths")
	}

	replayed, err := coverageService.Replay(context.Background(), before.ID, reviewer)
	if err != nil {
		t.Fatalf("replay of pre-suspension evaluation must still pass: %v", err)
	}
	if replayed.CoverageScore != 80 || replayed.InputHash != before.InputHash {
		t.Fatalf("historical evaluation must keep original score and snapshot, got score=%v", replayed.CoverageScore)
	}

	_, err = safeguardService.Resume(context.Background(), created.ID, dto.ResumeSafeguardRequest{
		VerifiedAt: time.Now().UTC(), EvidenceNote: "  ",
	}, reviewer)
	if !errors.As(err, &appErr) || appErr.Status != 422 {
		t.Fatalf("resume without evidence should return 422, got %v", err)
	}
	stillSuspended, getErr := safeguardService.Get(context.Background(), created.ID)
	if getErr != nil || stillSuspended.LifecycleState != "suspended" {
		t.Fatalf("safeguard should remain suspended without evidence, got %q err=%v", stillSuspended.LifecycleState, getErr)
	}
	_, err = safeguardService.Resume(context.Background(), created.ID, dto.ResumeSafeguardRequest{
		VerifiedAt: time.Now().UTC().Add(time.Hour), EvidenceNote: "reinstatement test report",
	}, reviewer)
	if !errors.As(err, &appErr) || appErr.Status != 422 {
		t.Fatalf("resume with future verification time should return 422, got %v", err)
	}

	resumeVerifiedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	resumed, err := safeguardService.Resume(context.Background(), created.ID, dto.ResumeSafeguardRequest{
		VerifiedAt: resumeVerifiedAt, EvidenceNote: "reinstatement test report RT-55",
	}, reviewer)
	if err != nil {
		t.Fatalf("resume safeguard: %v", err)
	}
	if resumed.LifecycleState != "active" {
		t.Fatalf("expected active after resume, got %q", resumed.LifecycleState)
	}
	if resumed.LastVerifiedAt == nil || !resumed.LastVerifiedAt.Equal(resumeVerifiedAt) {
		t.Fatalf("resume must record the supplied verification time, got %v", resumed.LastVerifiedAt)
	}
	if resumed.EvidenceNote != "reinstatement test report RT-55" {
		t.Fatalf("resume must store the supplied evidence, got %q", resumed.EvidenceNote)
	}

	after, _, err := coverageService.Run(context.Background(), dto.RunCoverageEvaluationRequest{ScenarioID: scenario.ID}, "suspend-flow-0003", engineer)
	if err != nil {
		t.Fatalf("run evaluation after resume: %v", err)
	}
	if after.CoverageScore != 80 {
		t.Fatalf("resumed safeguard must count again; score should be 80, got %v", after.CoverageScore)
	}

	historical, err := coverageService.Get(context.Background(), before.ID)
	if err != nil {
		t.Fatalf("reload historical evaluation: %v", err)
	}
	if historical.CoverageScore != 80 || historical.InputHash != before.InputHash {
		t.Fatalf("historical evaluation mutated after suspend/resume: score=%v", historical.CoverageScore)
	}
}
