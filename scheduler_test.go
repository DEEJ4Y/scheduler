package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// MockStore is a simple in-memory store for testing
type MockStore struct {
	mu        sync.Mutex
	jobs      map[interface{}]*Job
	idCounter int
}

func NewMockStore() *MockStore {
	return &MockStore{
		jobs: make(map[interface{}]*Job),
		idCounter: 1,
	}
}

func (s *MockStore) AddJob(job *Job) interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.idCounter
	s.idCounter++
	job.ID = id
	s.jobs[id] = job
	return id
}

func (s *MockStore) LockNext(ctx context.Context, lockUntil time.Time) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var nextJob *Job

	for _, job := range s.jobs {
		if job.SleepUntil == nil {
			continue
		}
		if job.SleepUntil.After(now) {
			continue
		}
		nextJob = job
		break
	}

	if nextJob == nil {
		return nil, nil
	}

	// Create a copy with the original sleepUntil
	jobCopy := *nextJob
	jobCopy.Data = make(map[string]interface{})
	for k, v := range nextJob.Data {
		jobCopy.Data[k] = v
	}

	// Lock the job
	nextJob.SleepUntil = &lockUntil

	return &jobCopy, nil
}

func (s *MockStore) Update(ctx context.Context, jobID interface{}, updates JobUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, exists := s.jobs[jobID]
	if !exists {
		return errors.New("job not found")
	}

	if updates.SleepUntil != nil {
		job.SleepUntil = *updates.SleepUntil
	}

	return nil
}

func (s *MockStore) Remove(ctx context.Context, jobID interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.jobs[jobID]; !exists {
		return errors.New("job not found")
	}

	delete(s.jobs, jobID)
	return nil
}

func (s *MockStore) CountJobs() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs)
}

func (s *MockStore) CountAvailableJobs() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	for _, job := range s.jobs {
		if job.SleepUntil != nil {
			count++
		}
	}
	return count
}

func TestNew(t *testing.T) {
	t.Run("requires store", func(t *testing.T) {
		_, err := New(Config{})
		if err == nil {
			t.Error("expected error when store is nil")
		}
	})

	t.Run("sets default lock duration", func(t *testing.T) {
		store := NewMockStore()
		sched, err := New(Config{Store: store})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sched.config.LockDuration != 10*time.Minute {
			t.Errorf("expected default lock duration of 10 minutes, got %v", sched.config.LockDuration)
		}
	})
}

func TestScheduler_StartStop(t *testing.T) {
	store := NewMockStore()
	sched, _ := New(Config{Store: store})

	ctx := context.Background()

	// Start
	if err := sched.Start(ctx); err != nil {
		t.Fatalf("failed to start: %v", err)
	}

	if !sched.IsRunning() {
		t.Error("scheduler should be running")
	}

	// Stop
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sched.Stop(stopCtx); err != nil {
		t.Fatalf("failed to stop: %v", err)
	}

	if sched.IsRunning() {
		t.Error("scheduler should not be running")
	}
}

func TestScheduler_ProcessOneTimeJob(t *testing.T) {
	store := NewMockStore()
	processed := 0

	sched, _ := New(Config{
		Store: store,
		OnDocument: func(ctx context.Context, job *Job) error {
			processed++
			return nil
		},
		NextDelay:    100 * time.Millisecond,
		LockDuration: 1 * time.Minute,
	})

	// Add a job
	now := time.Now()
	store.AddJob(&Job{
		SleepUntil: &now,
		Data:       map[string]interface{}{"test": "job1"},
	})

	ctx := context.Background()
	sched.Start(ctx)

	// Wait for processing
	time.Sleep(500 * time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sched.Stop(stopCtx)

	if processed != 1 {
		t.Errorf("expected 1 job processed, got %d", processed)
	}

	// Job should be marked as complete (sleepUntil = nil)
	if store.CountAvailableJobs() != 0 {
		t.Error("job should be marked as complete")
	}
}

func TestScheduler_ProcessRecurringJob(t *testing.T) {
	store := NewMockStore()
	processed := 0
	var mu sync.Mutex

	sched, _ := New(Config{
		Store: store,
		OnDocument: func(ctx context.Context, job *Job) error {
			mu.Lock()
			processed++
			mu.Unlock()
			return nil
		},
		NextDelay:      50 * time.Millisecond,
		ReprocessDelay: 0,
		LockDuration:   1 * time.Minute,
	})

	// Add a recurring job (every second)
	now := time.Now()
	store.AddJob(&Job{
		SleepUntil: &now,
		Interval:   "* * * * * *", // every second
		Data:       map[string]interface{}{"test": "recurring"},
	})

	ctx := context.Background()
	sched.Start(ctx)

	// Wait for multiple executions
	time.Sleep(3500 * time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sched.Stop(stopCtx)

	mu.Lock()
	count := processed
	mu.Unlock()

	// Should process at least 3 times (0s, 1s, 2s, 3s = 4 times)
	if count < 3 {
		t.Errorf("expected at least 3 executions, got %d", count)
	}
}

func TestScheduler_AutoRemove(t *testing.T) {
	store := NewMockStore()

	sched, _ := New(Config{
		Store:        store,
		NextDelay:    50 * time.Millisecond,
		LockDuration: 1 * time.Minute,
	})

	// Add a job with auto-remove
	now := time.Now()
	store.AddJob(&Job{
		SleepUntil: &now,
		AutoRemove: true,
		Data:       map[string]interface{}{"test": "auto-remove"},
	})

	initialCount := store.CountJobs()

	ctx := context.Background()
	sched.Start(ctx)

	// Wait for processing
	time.Sleep(300 * time.Millisecond)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sched.Stop(stopCtx)

	finalCount := store.CountJobs()

	if finalCount >= initialCount {
		t.Errorf("expected job to be removed, initial: %d, final: %d", initialCount, finalCount)
	}
}

func TestScheduler_OnIdleCallback(t *testing.T) {
	store := NewMockStore()
	idleCalled := 0

	sched, _ := New(Config{
		Store: store,
		OnIdle: func(ctx context.Context) error {
			idleCalled++
			return nil
		},
		NextDelay: 50 * time.Millisecond,
		IdleDelay: 100 * time.Millisecond,
	})

	ctx := context.Background()
	sched.Start(ctx)

	// Wait for idle state
	time.Sleep(500 * time.Millisecond)

	if !sched.IsIdle() {
		t.Error("scheduler should be idle")
	}

	if idleCalled != 1 {
		t.Errorf("expected OnIdle to be called once, got %d", idleCalled)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sched.Stop(stopCtx)
}

func TestScheduler_DeferredJob(t *testing.T) {
	store := NewMockStore()
	processed := false

	sched, _ := New(Config{
		Store: store,
		OnDocument: func(ctx context.Context, job *Job) error {
			processed = true
			return nil
		},
		NextDelay:    50 * time.Millisecond,
		LockDuration: 1 * time.Minute,
	})

	// Add a job scheduled for the future
	future := time.Now().Add(2 * time.Second)
	store.AddJob(&Job{
		SleepUntil: &future,
		Data:       map[string]interface{}{"test": "deferred"},
	})

	ctx := context.Background()
	sched.Start(ctx)

	// Should not be processed yet
	time.Sleep(500 * time.Millisecond)
	if processed {
		t.Error("job should not be processed before sleepUntil time")
	}

	// Wait until after sleepUntil
	time.Sleep(2 * time.Second)
	if !processed {
		t.Error("job should be processed after sleepUntil time")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sched.Stop(stopCtx)
}

func TestScheduler_RecurringWithExpiration(t *testing.T) {
	store := NewMockStore()
	processed := 0
	var mu sync.Mutex

	sched, _ := New(Config{
		Store: store,
		OnDocument: func(ctx context.Context, job *Job) error {
			mu.Lock()
			processed++
			mu.Unlock()
			return nil
		},
		NextDelay:      50 * time.Millisecond,
		ReprocessDelay: 0,
		LockDuration:   1 * time.Minute,
	})

	// Add a recurring job that expires after 2.5 seconds
	now := time.Now()
	expiry := now.Add(2500 * time.Millisecond)
	store.AddJob(&Job{
		SleepUntil:  &now,
		Interval:    "* * * * * *", // every second
		RepeatUntil: &expiry,
		Data:        map[string]interface{}{"test": "expiring"},
	})

	ctx := context.Background()
	sched.Start(ctx)

	// Wait past expiration
	time.Sleep(4 * time.Second)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sched.Stop(stopCtx)

	mu.Lock()
	count := processed
	mu.Unlock()

	// Should process approximately 2-3 times (at 0s, 1s, 2s)
	// but not more than 4 times
	if count > 4 {
		t.Errorf("job should have expired, but processed %d times", count)
	}

	// Job should be marked as complete after expiration
	if store.CountAvailableJobs() != 0 {
		t.Error("expired recurring job should be marked as complete")
	}
}
