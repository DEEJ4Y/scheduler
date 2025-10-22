package scheduler

import "time"

// Job represents a scheduled task in the system.
// It contains all the metadata needed to schedule and execute a job.
type Job struct {
	// ID is the unique identifier for the job (database-specific type).
	ID interface{}

	// SleepUntil is the time when the job should be processed.
	// - nil means the job has been completed or is not schedulable
	// - Past dates mean the job should be processed immediately
	// - Future dates mean the job should wait until that time
	SleepUntil *time.Time

	// Interval is a cron expression defining recurring job schedule.
	// Empty string means this is a one-time job.
	// Supports standard cron format: "* * * * * *" (second minute hour day month weekday)
	Interval string

	// RepeatUntil defines when a recurring job should stop repeating.
	// nil means the job repeats indefinitely.
	RepeatUntil *time.Time

	// AutoRemove indicates whether the job should be deleted after completion.
	// Only applies to non-recurring jobs or when a recurring job expires.
	AutoRemove bool

	// Data contains the job's custom fields and payload.
	// The structure depends on your application's needs.
	Data map[string]interface{}
}

// JobUpdate represents fields that can be updated on a job.
// This is used by the scheduler to reschedule jobs or mark them as complete.
type JobUpdate struct {
	// SleepUntil updates the job's next execution time.
	// Use a pointer to time.Time to distinguish between:
	// - nil: don't update this field
	// - pointer to nil time: set sleepUntil to null (job completed)
	// - pointer to valid time: set sleepUntil to that time
	SleepUntil **time.Time
}

// NewJobUpdate creates a JobUpdate that sets sleepUntil to the given time.
func NewJobUpdate(sleepUntil *time.Time) JobUpdate {
	return JobUpdate{
		SleepUntil: &sleepUntil,
	}
}
