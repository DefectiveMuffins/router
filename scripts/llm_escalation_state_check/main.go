// Command llm_escalation_state_check verifies durable async judge fencing.
// Run against a migrated disposable database with ROUTER_TEST_DATABASE_URL.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/escalationdashboard"
	"weave-os/router/internal/router/llmescalation"
)

func main() {
	dsn := os.Getenv("ROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		slog.Info("ROUTER_TEST_DATABASE_URL unset; skipping LLM escalation database check")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := check(ctx, dsn); err != nil {
		slog.Error("LLM escalation database check failed", "err", err)
		os.Exit(1)
	}
	slog.Info("LLM escalation database check passed: concurrent boundaries and deduplication, cadence, stale generations/checkpoints/lifetimes, lease expiry, application, continuations, tenant isolation")
}

func check(ctx context.Context, dsn string) (checkErr error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	repositories := postgres.NewRepository(pool, auth.NoOpEncryptor{})
	installation, err := repositories.Installations.Create(ctx, auth.CreateInstallationParams{ExternalID: uuid.NewString(), Name: "LLM escalation integration check"})
	if err != nil {
		return err
	}
	scope := sha256.Sum256([]byte(uuid.NewString()))
	cleanup := postgres.NewEscalationCheckFixture(pool, *installation, scope)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		checkErr = errors.Join(checkErr, cleanup.Cleanup(cleanupCtx))
	}()
	fixture, err := postgres.NewLLMEscalationCheckFixture(pool, installation.ID)
	if err != nil {
		return err
	}
	store := postgres.NewLLMEscalationRepo(pool)
	request := llmescalation.StartRequest{Scope: scope, InstallationID: installation.ID, InstructionFingerprint: sha256.Sum256([]byte("initial task")), Config: llmescalation.Config{Mode: llmescalation.ModeActive, Cadence: 3, Digest: "fixture-v1"}}
	session, err := store.Start(ctx, request)
	if err != nil {
		return err
	}
	mismatched := request
	mismatched.Config.Cadence = 4
	mismatched.InstructionFingerprint = sha256.Sum256([]byte("mismatched instruction"))
	if _, err := store.Start(ctx, mismatched); err == nil {
		return errors.New("scope accepted a different configuration")
	}
	session, err = store.Start(ctx, request)
	if err != nil || session.Generation != 1 || session.LastActivityAt.IsZero() {
		return fmt.Errorf("configuration mismatch changed session generation or activity time: %v", err)
	}
	boundary := sha256.Sum256([]byte("first response"))
	var concurrent errgroup.Group
	var ready sync.WaitGroup
	start := make(chan struct{})
	completions := make(chan llmescalation.Completion, 8)
	for range 8 {
		ready.Add(1)
		concurrent.Go(func() error {
			ready.Done()
			<-start
			completion, err := store.Complete(ctx, llmescalation.CompleteRequest{Session: session, Boundary: boundary, Capacity: true})
			if err == nil {
				completions <- completion
			}
			return err
		})
	}
	ready.Wait()
	close(start)
	if err := concurrent.Wait(); err != nil {
		return err
	}
	close(completions)
	claimed, duplicates := 0, 0
	for completion := range completions {
		if completion.Job != nil || completion.Session.CompletedTurns != 1 {
			return errors.New("same-boundary completion created a checkpoint or returned stale state")
		}
		if completion.Duplicate {
			duplicates++
		} else {
			claimed++
		}
	}
	if claimed != 1 || duplicates != 7 {
		return fmt.Errorf("same-boundary completions: %d claimed, %d duplicates", claimed, duplicates)
	}
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.CompletedTurns != 1 {
		return fmt.Errorf("duplicates counted %d completed turns", session.CompletedTurns)
	}
	initialSession := session
	var distinct [2]llmescalation.Completion
	var distinctConcurrent errgroup.Group
	var distinctReady sync.WaitGroup
	distinctStart := make(chan struct{})
	for i, label := range []string{"second", "third"} {
		distinctReady.Add(1)
		distinctConcurrent.Go(func() error {
			distinctReady.Done()
			<-distinctStart
			var err error
			distinct[i], err = store.Complete(ctx, llmescalation.CompleteRequest{
				Session: initialSession, Boundary: sha256.Sum256([]byte(label)), RequestID: label, Capacity: true,
			})
			return err
		})
	}
	distinctReady.Wait()
	close(distinctStart)
	if err := distinctConcurrent.Wait(); err != nil {
		return err
	}
	var checkpoint llmescalation.Completion
	seenTurns := make(map[int64]struct{}, len(distinct))
	for _, completion := range distinct {
		if completion.Duplicate {
			return errors.New("distinct boundary was deduplicated")
		}
		seenTurns[completion.Session.CompletedTurns] = struct{}{}
		if completion.Job != nil {
			if completion.Session.CompletedTurns != 3 || checkpoint.Job != nil {
				return errors.New("concurrent distinct boundaries claimed the wrong checkpoint")
			}
			checkpoint = completion
		}
	}
	_, sawSecond := seenTurns[2]
	_, sawThird := seenTurns[3]
	if len(seenTurns) != 2 || !sawSecond || !sawThird || checkpoint.Job == nil {
		return fmt.Errorf("concurrent distinct boundaries returned unexpected turn counts or checkpoints: turns=%d second=%t third=%t checkpoint=%t", len(seenTurns), sawSecond, sawThird, checkpoint.Job != nil)
	}
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.CompletedTurns != 3 || session.LatestCheckpoint != 3 || session.JudgeCalls != 1 {
		return fmt.Errorf("concurrent distinct boundaries changed cadence: %+v", session)
	}
	for _, label := range []string{"first response", "second", "third"} {
		duplicate, err := store.Complete(ctx, llmescalation.CompleteRequest{Session: initialSession, Boundary: sha256.Sum256([]byte(label)), Capacity: true})
		if err != nil || !duplicate.Duplicate || duplicate.Job != nil || duplicate.Session.CompletedTurns != 3 || duplicate.Session.LatestCheckpoint != 3 || duplicate.Session.JudgeCalls != 1 {
			return fmt.Errorf("duplicate %q did not return the current session without a job: %v", label, err)
		}
	}
	finish := func(label string, capacity bool) (llmescalation.Completion, error) {
		return store.Complete(ctx, llmescalation.CompleteRequest{Session: session, Boundary: sha256.Sum256([]byte(label)), RequestID: label, Capacity: capacity})
	}
	job := *checkpoint.Job
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.Pending != nil {
		return errors.New("running judge was visible as ready")
	}
	oldGeneration := session
	request.InstructionFingerprint = sha256.Sum256([]byte("new instruction"))
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.CompletedTurns != 3 || session.Generation != 2 {
		return errors.New("new instruction reset cadence or failed to advance generation")
	}
	if _, err := store.Complete(ctx, llmescalation.CompleteRequest{
		Session: oldGeneration, Boundary: sha256.Sum256([]byte("fourth")), Capacity: true,
	}); !errors.Is(err, llmescalation.ErrStale) {
		return fmt.Errorf("old generation completion was accepted: %w", err)
	}
	if err := store.FinishJob(ctx, job, llmescalation.Judgment{Escalate: true}, llmescalation.FailureNone); err != nil {
		return err
	}
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.Pending != nil {
		return errors.New("old instruction judgment became pending")
	}
	stale, found, err := store.GetJob(ctx, installation.ID, job.ID)
	if err != nil {
		return err
	}
	if !found || stale.Status != llmescalation.JobStale {
		return errors.New("old instruction judgment not marked stale")
	}
	for _, label := range []string{"fourth", "fifth"} {
		completion, err := finish(label, true)
		if err != nil || completion.Duplicate {
			return fmt.Errorf("fresh generation completion %q failed or duplicated: %v", label, err)
		}
	}
	checkpoint, err = finish("sixth", true)
	if err != nil {
		return err
	}
	if checkpoint.Job == nil {
		return errors.New("sixth completion did not claim judge")
	}
	job = *checkpoint.Job
	if err := fixture.ExpireJob(ctx, job.ID); err != nil {
		return err
	}
	if err := store.FinishJob(ctx, job, llmescalation.Judgment{Escalate: true}, llmescalation.FailureNone); err != nil {
		return err
	}
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.Pending != nil {
		return errors.New("expired judge lease became pending")
	}
	for _, label := range []string{"seventh", "eighth"} {
		if _, err := finish(label, true); err != nil {
			return err
		}
	}
	checkpoint, err = finish("ninth", true)
	if err != nil {
		return err
	}
	if checkpoint.Job == nil {
		return errors.New("ninth completion did not claim judge")
	}
	job = *checkpoint.Job
	if err := store.FinishJob(ctx, job, llmescalation.Judgment{Escalate: true}, llmescalation.FailureNone); err != nil {
		return err
	}
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.Pending == nil {
		return errors.New("fresh positive judgment unavailable")
	}
	applyRequest := llmescalation.ApplyRequest{Session: session, JobID: job.ID, Floor: escalation.Maximum, RequestID: "apply-request", Turn: 10}
	if err := store.RecordNoTarget(ctx, applyRequest); err != nil {
		return err
	}
	session, err = store.Start(ctx, request)
	if err != nil || session.Pending == nil || session.Pending.ID != job.ID {
		return fmt.Errorf("no-target annotation consumed the positive verdict: %v", err)
	}
	annotatedJob, found, err := store.GetJob(ctx, installation.ID, job.ID)
	if err != nil || !found || annotatedJob.Failure != llmescalation.FailureNoTarget {
		return fmt.Errorf("no-target annotation was not persisted: %v", err)
	}
	applyRequest.Session = session
	if applied, err := store.Apply(ctx, applyRequest); err != nil || !applied {
		return fmt.Errorf("valid floor was not applied: %w", err)
	}
	applyRequest.RequestID = "duplicate-apply-request"
	applyRequest.Turn = 11
	if applied, err := store.Apply(ctx, applyRequest); err != nil || !applied {
		return fmt.Errorf("repeated floor application failed: %v", err)
	}
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.Floor != escalation.Maximum || session.Pending != nil {
		return errors.New("floor/application persistence failed")
	}
	appliedJob, _, err := store.GetJob(ctx, installation.ID, job.ID)
	if err != nil {
		return err
	}
	if appliedJob.AppliedRequestID != "apply-request" || appliedJob.AppliedTurn == nil || *appliedJob.AppliedTurn != 10 || appliedJob.Status != llmescalation.JobApplied {
		return errors.New("application attribution missing")
	}
	dashboardCapturedAt := time.Now().UTC()
	storedDashboard, err := postgres.NewEscalationDashboardRepo(pool).CreateSnapshot(ctx, escalationdashboard.Filter{
		Service:        escalationdashboard.ServiceSwitchyard,
		SessionOutcome: escalationdashboard.SessionOutcomeNoEvaluation,
		Limit:          50,
		CapturedAt:     dashboardCapturedAt,
		ExpiresAt:      dashboardCapturedAt.Add(time.Minute),
	})
	if err != nil {
		return err
	}
	dashboard := storedDashboard.Snapshot
	if dashboard.Summary.ObservedSessions != 1 || dashboard.Summary.Evaluations != 1 || dashboard.Summary.Recommendations != 1 || dashboard.Summary.EscalationsApplied != 1 || dashboard.Summary.InvalidEvaluations < 2 {
		return fmt.Errorf("Switchyard dashboard metrics did not reconcile: %+v", dashboard.Summary)
	}
	if dashboard.MatchingSessions != 0 || len(dashboard.Sessions) != 0 {
		return errors.New("session outcome filter changed dashboard cohort or returned a mismatched row")
	}
	request.InstructionFingerprint = sha256.Sum256([]byte("yet another instruction"))
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.Floor != escalation.Maximum {
		return errors.New("new instruction erased accepted floor")
	}
	oldLifetimeSession := session
	activation := sha256.Sum256([]byte(uuid.NewString()))
	if err := store.SaveContinuation(ctx, llmescalation.ContinuationRequest{Session: session, Activation: activation, ResponseID: "response-id", History: json.RawMessage(`[]`)}); err != nil {
		return err
	}
	if _, found, err := store.Continuation(ctx, activation, "response-id"); err != nil || !found {
		return fmt.Errorf("continuation missing: %w", err)
	}
	if _, found, err := store.Continuation(ctx, sha256.Sum256([]byte("other-key")), "response-id"); err != nil || found {
		return fmt.Errorf("continuation isolation failed: %w", err)
	}
	if _, found, err := store.GetJob(ctx, uuid.NewString(), job.ID); err != nil || found {
		return fmt.Errorf("job tenant isolation failed: %w", err)
	}
	oldLifetime := session.Lifetime
	if err := fixture.ExpireSession(ctx, oldLifetime); err != nil {
		return err
	}
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.Lifetime == oldLifetime || session.Floor != "" || session.CompletedTurns != 0 {
		return errors.New("expired lifetime resurrected")
	}
	if _, err := store.Complete(ctx, llmescalation.CompleteRequest{
		Session: oldLifetimeSession, Boundary: sha256.Sum256([]byte("new-first")), Capacity: true,
	}); !errors.Is(err, llmescalation.ErrStale) {
		return fmt.Errorf("expired lifetime completion was accepted: %w", err)
	}
	if err := store.FinishJob(ctx, job, llmescalation.Judgment{Escalate: true}, llmescalation.FailureNone); err != nil {
		return err
	}
	if _, found, err := store.Continuation(ctx, activation, "response-id"); err != nil || found {
		return fmt.Errorf("expired continuation survived: %w", err)
	}
	for _, label := range []string{"new-first", "new-second"} {
		completion, err := finish(label, true)
		if err != nil || completion.Duplicate {
			return fmt.Errorf("new lifetime completion %q failed or duplicated: %v", label, err)
		}
	}
	checkpoint, err = finish("new-third", true)
	if err != nil {
		return err
	}
	if checkpoint.Job == nil {
		return errors.New("recreated lifetime missing checkpoint")
	}
	job = *checkpoint.Job
	for _, label := range []string{"new-fourth", "new-fifth"} {
		if _, err := finish(label, true); err != nil {
			return err
		}
	}
	checkpoint, err = finish("new-sixth", true)
	if err != nil {
		return err
	}
	if checkpoint.Job != nil {
		return errors.New("overlapping paid judge was admitted")
	}
	if err := store.FinishJob(ctx, job, llmescalation.Judgment{Escalate: true}, llmescalation.FailureNone); err != nil {
		return err
	}
	session, err = store.Start(ctx, request)
	if err != nil {
		return err
	}
	if session.Pending != nil {
		return errors.New("superseded checkpoint was applicable")
	}
	return nil
}
