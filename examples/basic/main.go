package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/DEEJ4Y/scheduler"
)

// InMemoryStore is a simple in-memory implementation of JobStore for demonstration.
// This is NOT suitable for production use - it doesn't handle persistence,
// distributed locking, or crash recovery.
type InMemoryStore struct {
	mu   sync.Mutex
	jobs map[interface{}]*scheduler.Job
	idCounter int
}

func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		jobs: make(map[interface{}]*scheduler.Job),
		idCounter: 1,
	}
}

func (s *InMemoryStore) AddJob(job *scheduler.Job) interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.idCounter
	s.idCounter++
	job.ID = id
	s.jobs[id] = job
	return id
}

func (s *InMemoryStore) LockNext(ctx context.Context, lockUntil time.Time) (*scheduler.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var nextJob *scheduler.Job

	// Find the next available job
	for _, job := range s.jobs {
		if job.SleepUntil == nil {
			continue
		}
		if job.SleepUntil.After(now) {
			continue
		}
		// Found an available job
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

	// Lock the job by updating sleepUntil
	nextJob.SleepUntil = &lockUntil

	return &jobCopy, nil
}

func (s *InMemoryStore) Update(ctx context.Context, jobID interface{}, updates scheduler.JobUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, exists := s.jobs[jobID]
	if !exists {
		return fmt.Errorf("job not found: %v", jobID)
	}

	if updates.SleepUntil != nil {
		job.SleepUntil = *updates.SleepUntil
	}

	return nil
}

func (s *InMemoryStore) Remove(ctx context.Context, jobID interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.jobs[jobID]; !exists {
		return fmt.Errorf("job not found: %v", jobID)
	}

	delete(s.jobs, jobID)
	return nil
}

func main() {
	ctx := context.Background()

	// Create in-memory store
	store := NewInMemoryStore()

	// Create scheduler
	sched, err := scheduler.New(scheduler.Config{
		Store: store,
		OnDocument: func(ctx context.Context, job *scheduler.Job) error {
			fmt.Printf("Processing job %v: %v\n", job.ID, job.Data["name"])

			// Simulate some work
			time.Sleep(500 * time.Millisecond)

			return nil
		},
		OnStart: func(ctx context.Context) error {
			fmt.Println("Scheduler started!")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			fmt.Println("Scheduler stopped!")
			return nil
		},
		OnIdle: func(ctx context.Context) error {
			fmt.Println("All jobs processed, entering idle mode...")
			return nil
		},
		OnError: func(ctx context.Context, err error) {
			fmt.Printf("Error: %v\n", err)
		},
		NextDelay:      500 * time.Millisecond,
		ReprocessDelay: 1 * time.Second,
		IdleDelay:      5 * time.Second,
		LockDuration:   1 * time.Minute,
	})
	if err != nil {
		log.Fatalf("Failed to create scheduler: %v", err)
	}

	// Add some sample jobs
	now := time.Now()

	// One-time job (immediate)
	store.AddJob(&scheduler.Job{
		SleepUntil: &now,
		Data: map[string]interface{}{
			"name": "Immediate job",
		},
	})

	// Deferred job (3 seconds)
	future := now.Add(3 * time.Second)
	store.AddJob(&scheduler.Job{
		SleepUntil: &future,
		Data: map[string]interface{}{
			"name": "Deferred job (3s)",
		},
	})

	// Recurring job (every 2 seconds, expires after 10 seconds)
	expiry := now.Add(10 * time.Second)
	store.AddJob(&scheduler.Job{
		SleepUntil:  &now,
		Interval:    "*/2 * * * * *", // every 2 seconds
		RepeatUntil: &expiry,
		Data: map[string]interface{}{
			"name": "Recurring job (every 2s, expires in 10s)",
		},
	})

	// Auto-remove job
	autoRemoveTime := now.Add(5 * time.Second)
	store.AddJob(&scheduler.Job{
		SleepUntil: &autoRemoveTime,
		AutoRemove: true,
		Data: map[string]interface{}{
			"name": "Auto-remove job (5s)",
		},
	})

	fmt.Println("Starting scheduler with sample jobs...")

	// Start scheduler
	if err := sched.Start(ctx); err != nil {
		log.Fatalf("Failed to start scheduler: %v", err)
	}

	// Run for 30 seconds then stop
	time.Sleep(30 * time.Second)

	// Stop scheduler
	fmt.Println("\nStopping scheduler...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sched.Stop(shutdownCtx); err != nil {
		log.Printf("Error during shutdown: %v", err)
	}

	fmt.Println("Done!")
}
