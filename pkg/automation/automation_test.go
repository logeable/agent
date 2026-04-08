package automation

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoopJobRunnerRetriesAndStoresRun(t *testing.T) {
	var calls atomic.Int64
	runs := NewMemoryRunStore()
	runner := LoopJobRunner{
		RunStore: runs,
		Factory: func(ctx context.Context, job JobSpec) (func(context.Context, string, string) (string, error), error) {
			return func(_ context.Context, _ string, _ string) (string, error) {
				if calls.Add(1) == 1 {
					return "", fmt.Errorf("boom")
				}
				return "done", nil
			}, nil
		},
	}

	record, err := runner.Run(context.Background(), JobSpec{
		ID:         "job-1",
		Prompt:     "do it",
		SessionKey: "session-1",
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !record.Success || record.Attempts != 2 {
		t.Fatalf("record = %+v, want success after retry", record)
	}
	stored, err := runs.List(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored run count = %d, want 1", len(stored))
	}
}

func TestMemorySchedulerRunsJobsAndStopsOnCancel(t *testing.T) {
	jobs := NewMemoryJobStore()
	runs := NewMemoryRunStore()
	var count atomic.Int64
	runner := RunFunc(func(ctx context.Context, job JobSpec) (RunRecord, error) {
		count.Add(1)
		return RunRecord{
			JobID:      job.ID,
			StartedAt:  time.Now(),
			FinishedAt: time.Now(),
			Success:    true,
		}, nil
	})
	scheduler := NewMemoryScheduler(jobs, runs, runner, nil)
	if err := scheduler.Add(JobSpec{
		ID:       "job-1",
		Prompt:   "ping",
		Enabled:  true,
		Interval: 100 * time.Millisecond,
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- scheduler.Start(ctx)
	}()

	deadline := time.Now().Add(800 * time.Millisecond)
	for count.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	if count.Load() == 0 {
		t.Fatal("scheduler did not trigger any jobs")
	}
	if err := <-done; err != context.Canceled {
		t.Fatalf("Start() error = %v, want context.Canceled", err)
	}
}

func TestMemorySchedulerPauseCancelsRunningJob(t *testing.T) {
	jobs := NewMemoryJobStore()
	runs := NewMemoryRunStore()
	cancelled := make(chan struct{}, 1)
	runner := RunFunc(func(ctx context.Context, job JobSpec) (RunRecord, error) {
		<-ctx.Done()
		cancelled <- struct{}{}
		return RunRecord{JobID: job.ID, StartedAt: time.Now(), FinishedAt: time.Now(), Cancelled: true, Err: ctx.Err()}, ctx.Err()
	})
	scheduler := NewMemoryScheduler(jobs, runs, runner, nil)
	if err := scheduler.Add(JobSpec{
		ID:       "job-1",
		Prompt:   "ping",
		Enabled:  true,
		Interval: 50 * time.Millisecond,
	}); err != nil {
		t.Fatalf("Add() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go scheduler.Start(ctx)
	if err := scheduler.Trigger(ctx, "job-1"); err != nil {
		t.Fatalf("Trigger() error = %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	if err := scheduler.Pause("job-1"); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Pause() did not cancel running job")
	}
}
