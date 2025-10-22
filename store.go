package scheduler

import (
	"context"
	"time"
)

// JobStore defines the interface for database operations needed by the scheduler.
// Any database can implement this interface to work with the scheduler.
//
// Implementations must ensure thread-safety and support atomic operations,
// particularly for LockNext which is critical for preventing race conditions
// in distributed environments.
type JobStore interface {
	// LockNext atomically finds and locks the next available job.
	// It should:
	// 1. Find a job where sleepUntil exists, is not null, and is <= current time
	// 2. Atomically update that job's sleepUntil to lockUntil (to lock it)
	// 3. Return the job data BEFORE the update (original sleepUntil value)
	//
	// Returns nil job if no job is available for processing.
	//
	// The lockUntil time ensures that if a worker crashes, the job will
	// become available again after the lock expires.
	LockNext(ctx context.Context, lockUntil time.Time) (*Job, error)

	// Update modifies a job's fields.
	// Used to reschedule jobs, mark them as complete, or update metadata.
	Update(ctx context.Context, jobID interface{}, updates JobUpdate) error

	// Remove deletes a job from the store.
	// Used for jobs with AutoRemove set to true.
	Remove(ctx context.Context, jobID interface{}) error
}
