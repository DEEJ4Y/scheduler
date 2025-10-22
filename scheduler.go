package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Config holds the configuration for a Scheduler.
type Config struct {
	// Store is the required database abstraction layer.
	Store JobStore

	// Event Handlers (all optional)

	// OnDocument is called when a job is ready to be processed.
	// The job is already locked when this is called.
	OnDocument func(ctx context.Context, job *Job) error

	// OnStart is called when the scheduler starts.
	OnStart func(ctx context.Context) error

	// OnStop is called when the scheduler stops.
	OnStop func(ctx context.Context) error

	// OnIdle is called when all jobs have been processed and the scheduler
	// enters idle state. It's only called once when transitioning to idle.
	OnIdle func(ctx context.Context) error

	// OnError is called when an error occurs during processing.
	// If OnError is not set, errors are silently ignored.
	OnError func(ctx context.Context, err error)

	// Timing Configuration

	// NextDelay is the duration to wait before processing the next job.
	// Default: 0 (process immediately)
	NextDelay time.Duration

	// ReprocessDelay is the duration to wait before reprocessing a recurring job.
	// This is added to the current time before calculating the next execution.
	// Default: 0
	ReprocessDelay time.Duration

	// IdleDelay is the duration to wait when no jobs are available.
	// Default: 0
	IdleDelay time.Duration

	// LockDuration is how long a job is locked during processing.
	// If a worker crashes, the job becomes available again after this duration.
	// Default: 10 minutes
	LockDuration time.Duration
}

// Scheduler is the main job scheduler that processes jobs from a JobStore.
type Scheduler struct {
	store  JobStore
	config Config

	// State tracking
	running    atomic.Bool
	processing atomic.Bool
	idle       atomic.Bool

	// Lifecycle management
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// New creates a new Scheduler with the given configuration.
// Returns an error if the configuration is invalid.
func New(config Config) (*Scheduler, error) {
	if config.Store == nil {
		return nil, errors.New("store is required")
	}

	// Set defaults
	if config.LockDuration == 0 {
		config.LockDuration = 10 * time.Minute
	}

	return &Scheduler{
		store:  config.Store,
		config: config,
	}, nil
}

// Start begins processing jobs from the store.
// It's safe to call Start multiple times; subsequent calls are no-ops.
// The scheduler runs until Stop is called or the context is canceled.
func (s *Scheduler) Start(ctx context.Context) error {
	// Only start once
	if s.running.Swap(true) {
		return nil
	}

	// Create a new context for this run
	s.ctx, s.cancel = context.WithCancel(ctx)

	// Call OnStart handler
	if s.config.OnStart != nil {
		if err := s.config.OnStart(s.ctx); err != nil {
			s.running.Store(false)
			return fmt.Errorf("OnStart handler failed: %w", err)
		}
	}

	// Start the processing loop
	s.wg.Add(1)
	go s.run()

	return nil
}

// Stop gracefully stops the scheduler.
// It waits for the current job to finish processing before returning.
// It's safe to call Stop multiple times.
func (s *Scheduler) Stop(ctx context.Context) error {
	var err error
	s.stopOnce.Do(func() {
		// Signal shutdown
		s.running.Store(false)
		if s.cancel != nil {
			s.cancel()
		}

		// Wait for processing to complete
		done := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Clean shutdown
		case <-ctx.Done():
			err = ctx.Err()
			return
		}

		// Call OnStop handler
		if s.config.OnStop != nil {
			if stopErr := s.config.OnStop(context.Background()); stopErr != nil && err == nil {
				err = fmt.Errorf("OnStop handler failed: %w", stopErr)
			}
		}
	})
	return err
}

// IsRunning returns true if the scheduler is running.
func (s *Scheduler) IsRunning() bool {
	return s.running.Load()
}

// IsProcessing returns true if a job is currently being processed.
func (s *Scheduler) IsProcessing() bool {
	return s.processing.Load()
}

// IsIdle returns true if the scheduler is idle (no jobs available).
func (s *Scheduler) IsIdle() bool {
	return s.idle.Load()
}

// run is the main processing loop.
func (s *Scheduler) run() {
	defer s.wg.Done()

	for s.running.Load() {
		// Wait before processing next job
		if s.config.NextDelay > 0 {
			select {
			case <-time.After(s.config.NextDelay):
			case <-s.ctx.Done():
				return
			}
		}

		// Check if still running after delay
		if !s.running.Load() {
			return
		}

		// Process next job
		s.tick()
	}
}

// tick processes a single job iteration.
func (s *Scheduler) tick() {
	s.processing.Store(true)
	defer s.processing.Store(false)

	// Calculate lock expiration time
	lockUntil := time.Now().Add(s.config.LockDuration)

	// Try to lock next job
	job, err := s.store.LockNext(s.ctx, lockUntil)
	if err != nil {
		s.handleError(fmt.Errorf("failed to lock next job: %w", err))
		return
	}

	// No job available
	if job == nil {
		// Trigger OnIdle only once when transitioning to idle state
		if !s.idle.Swap(true) {
			if s.config.OnIdle != nil {
				if err := s.config.OnIdle(s.ctx); err != nil {
					s.handleError(fmt.Errorf("OnIdle handler failed: %w", err))
				}
			}
		}

		// Sleep during idle period
		if s.config.IdleDelay > 0 {
			select {
			case <-time.After(s.config.IdleDelay):
			case <-s.ctx.Done():
			}
		}
		return
	}

	// We have a job, no longer idle
	s.idle.Store(false)

	// Process the job
	if s.config.OnDocument != nil {
		if err := s.config.OnDocument(s.ctx, job); err != nil {
			s.handleError(fmt.Errorf("OnDocument handler failed: %w", err))
		}
	}

	// Reschedule the job
	if err := s.reschedule(job); err != nil {
		s.handleError(fmt.Errorf("failed to reschedule job: %w", err))
	}
}

// reschedule determines what to do with a job after processing.
func (s *Scheduler) reschedule(job *Job) error {
	// Calculate next execution time for recurring jobs
	nextStart, err := calculateNextStart(job, s.config.ReprocessDelay)
	if err != nil {
		// Invalid cron expression - mark job as completed
		nextStart = nil
	}

	// Decide what to do based on next start time and auto-remove setting
	if nextStart == nil && job.AutoRemove {
		// Job is complete and should be removed
		return s.store.Remove(s.ctx, job.ID)
	} else if nextStart == nil {
		// Job is complete, set sleepUntil to nil
		var nilTime *time.Time
		return s.store.Update(s.ctx, job.ID, NewJobUpdate(nilTime))
	} else {
		// Reschedule for next execution
		return s.store.Update(s.ctx, job.ID, NewJobUpdate(nextStart))
	}
}

// handleError calls the OnError handler if configured.
func (s *Scheduler) handleError(err error) {
	if s.config.OnError != nil {
		s.config.OnError(s.ctx, err)
	}
}
