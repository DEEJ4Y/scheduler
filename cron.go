package scheduler

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// calculateNextStart calculates the next execution time for a job.
// Returns nil if:
// - The job is not recurring (no interval)
// - The job has expired (past repeatUntil)
// - The interval expression is invalid
//
// For recurring jobs, it:
// 1. Starts from the current sleepUntil time (or now if nil)
// 2. Adds the reprocessDelay
// 3. Finds the next occurrence based on the cron expression
// 4. Ensures we don't process old jobs multiple times (returns now if next is in past)
func calculateNextStart(job *Job, reprocessDelay time.Duration) (*time.Time, error) {
	// Not a recurring job
	if job.Interval == "" {
		return nil, nil
	}

	// Parse the cron expression
	parser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(job.Interval)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression %q: %w", job.Interval, err)
	}

	// Determine the base time for calculating next execution
	available := time.Now()
	if job.SleepUntil != nil {
		available = *job.SleepUntil
	}

	// Add reprocess delay before calculating next execution
	available = available.Add(reprocessDelay)

	// Calculate next execution time
	next := schedule.Next(available)

	// Check if past repeatUntil deadline
	if job.RepeatUntil != nil && next.After(*job.RepeatUntil) {
		return nil, nil
	}

	// Don't process old recurring jobs multiple times
	// If the next execution is in the past, execute now instead
	now := time.Now()
	if next.Before(now) {
		next = now
	}

	return &next, nil
}
