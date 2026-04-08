package automation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/logeable/agent/pkg/orchestration"
)

// JobSpec describes one scheduled job.
type JobSpec struct {
	ID               string
	Name             string
	Prompt           string
	Profile          string
	SessionKey       string
	Interval         time.Duration
	Enabled          bool
	Timeout          time.Duration
	MaxRetries       int
	EnableDelegation bool
	EnableCodeExec   bool
}

// RunRecord captures one job execution attempt.
type RunRecord struct {
	JobID      string
	StartedAt  time.Time
	FinishedAt time.Time
	Attempts   int
	Success    bool
	Response   string
	Err        error
	Cancelled  bool
}

type Scheduler interface {
	Start(ctx context.Context) error
	Add(JobSpec) error
	Pause(id string) error
	Trigger(ctx context.Context, id string) error
}

type JobRunner interface {
	Run(ctx context.Context, job JobSpec) (RunRecord, error)
}

type JobStore interface {
	List(context.Context) ([]JobSpec, error)
	Upsert(context.Context, JobSpec) error
	Delete(context.Context, string) error
}

type RunStore interface {
	Append(context.Context, RunRecord) error
	List(context.Context, string) ([]RunRecord, error)
}

// RunFunc adapts a function to JobRunner.
type RunFunc func(ctx context.Context, job JobSpec) (RunRecord, error)

func (f RunFunc) Run(ctx context.Context, job JobSpec) (RunRecord, error) {
	return f(ctx, job)
}

// MemoryJobStore keeps job specs in process memory.
type MemoryJobStore struct {
	mu   sync.RWMutex
	jobs map[string]JobSpec
}

func NewMemoryJobStore() *MemoryJobStore {
	return &MemoryJobStore{jobs: make(map[string]JobSpec)}
}

func (s *MemoryJobStore) List(_ context.Context) ([]JobSpec, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]JobSpec, 0, len(s.jobs))
	for _, job := range s.jobs {
		out = append(out, job)
	}
	return out, nil
}

func (s *MemoryJobStore) Upsert(_ context.Context, job JobSpec) error {
	if job.ID == "" {
		return fmt.Errorf("job id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
	return nil
}

func (s *MemoryJobStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
	return nil
}

// MemoryRunStore keeps run records in process memory.
type MemoryRunStore struct {
	mu   sync.RWMutex
	runs map[string][]RunRecord
}

func NewMemoryRunStore() *MemoryRunStore {
	return &MemoryRunStore{runs: make(map[string][]RunRecord)}
}

func (s *MemoryRunStore) Append(_ context.Context, run RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.JobID] = append(s.runs[run.JobID], run)
	return nil
}

func (s *MemoryRunStore) List(_ context.Context, jobID string) ([]RunRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]RunRecord(nil), s.runs[jobID]...), nil
}

// LoopJobRunner executes job prompts through a provided loop factory.
type LoopJobRunner struct {
	Factory  func(ctx context.Context, job JobSpec) (func(context.Context, string, string) (string, error), error)
	RunStore RunStore
	Events   *orchestration.EventBus
}

func (r LoopJobRunner) Run(ctx context.Context, job JobSpec) (RunRecord, error) {
	record := RunRecord{
		JobID:     job.ID,
		StartedAt: time.Now(),
	}
	if r.Factory == nil {
		record.Err = fmt.Errorf("job runner factory is nil")
		record.FinishedAt = time.Now()
		return record, record.Err
	}

	if r.Events != nil {
		r.Events.Emit(orchestration.Event{
			Kind: orchestration.EventJobStarted,
			Payload: map[string]any{
				"job_id":      job.ID,
				"session_key": job.SessionKey,
			},
		})
	}

	var lastErr error
	maxAttempts := job.MaxRetries + 1
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		record.Attempts = attempt
		runCtx := ctx
		var cancel context.CancelFunc
		if job.Timeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, job.Timeout)
		}
		if cancel != nil {
			defer cancel()
		}

		process, err := r.Factory(runCtx, job)
		if err != nil {
			lastErr = err
		} else {
			response, runErr := process(runCtx, job.SessionKey, job.Prompt)
			record.Response = response
			lastErr = runErr
		}
		if lastErr == nil {
			record.Success = true
			break
		}
		if runCtx.Err() != nil {
			record.Cancelled = true
			break
		}
	}

	record.Err = lastErr
	record.FinishedAt = time.Now()
	if r.RunStore != nil {
		_ = r.RunStore.Append(ctx, record)
	}
	if r.Events != nil {
		r.Events.Emit(orchestration.Event{
			Kind: orchestration.EventJobFinished,
			Payload: map[string]any{
				"job_id":    job.ID,
				"success":   record.Success,
				"attempts":  record.Attempts,
				"cancelled": record.Cancelled,
				"error":     errorString(record.Err),
			},
		})
	}
	return record, record.Err
}

// MemoryScheduler is a fixed-interval in-process scheduler.
type MemoryScheduler struct {
	Jobs    JobStore
	Runs    RunStore
	Runner  JobRunner
	Events  *orchestration.EventBus
	mu      sync.Mutex
	nextRun map[string]time.Time
	paused  map[string]bool
	running map[string]context.CancelFunc
}

func NewMemoryScheduler(jobStore JobStore, runStore RunStore, runner JobRunner, events *orchestration.EventBus) *MemoryScheduler {
	return &MemoryScheduler{
		Jobs:    jobStore,
		Runs:    runStore,
		Runner:  runner,
		Events:  events,
		nextRun: make(map[string]time.Time),
		paused:  make(map[string]bool),
		running: make(map[string]context.CancelFunc),
	}
}

func (s *MemoryScheduler) Start(ctx context.Context) error {
	if s.Jobs == nil {
		return fmt.Errorf("scheduler job store is nil")
	}
	if s.Runner == nil {
		return fmt.Errorf("scheduler job runner is nil")
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.stopRunning()
			return ctx.Err()
		case now := <-ticker.C:
			jobs, err := s.Jobs.List(ctx)
			if err != nil {
				return err
			}
			for _, job := range jobs {
				if !job.Enabled || job.Interval <= 0 || s.isPaused(job.ID) {
					continue
				}
				if !s.due(job.ID, now, job.Interval) {
					continue
				}
				s.launch(ctx, job)
			}
		}
	}
}

func (s *MemoryScheduler) Add(job JobSpec) error {
	if s.Jobs == nil {
		return fmt.Errorf("scheduler job store is nil")
	}
	if job.Enabled == false && job.Interval == 0 && job.ID == "" {
		return fmt.Errorf("job id is required")
	}
	return s.Jobs.Upsert(context.Background(), job)
}

func (s *MemoryScheduler) Pause(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused[id] = true
	if cancel := s.running[id]; cancel != nil {
		cancel()
		delete(s.running, id)
	}
	return nil
}

func (s *MemoryScheduler) Trigger(ctx context.Context, id string) error {
	if s.Jobs == nil {
		return fmt.Errorf("scheduler job store is nil")
	}
	jobs, err := s.Jobs.List(ctx)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.ID != id {
			continue
		}
		s.launch(ctx, job)
		return nil
	}
	return fmt.Errorf("job %q not found", id)
}

func (s *MemoryScheduler) due(id string, now time.Time, interval time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, ok := s.nextRun[id]
	if !ok {
		s.nextRun[id] = now.Add(interval)
		return false
	}
	if now.Before(next) {
		return false
	}
	s.nextRun[id] = now.Add(interval)
	return true
}

func (s *MemoryScheduler) isPaused(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused[id]
}

func (s *MemoryScheduler) launch(ctx context.Context, job JobSpec) {
	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.running[job.ID] = cancel
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.running, job.ID)
			s.mu.Unlock()
		}()
		_, _ = s.Runner.Run(runCtx, job)
	}()
}

func (s *MemoryScheduler) stopRunning() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, cancel := range s.running {
		cancel()
		delete(s.running, id)
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
