package scheduler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConcurrentSchedulers validates that multiple schedulers can process jobs concurrently
// without duplicate executions using an in-memory mock store.
func TestConcurrentSchedulers(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrency test in short mode")
	}

	const (
		numSchedulers = 50    // Number of concurrent scheduler instances
		numJobs       = 5000  // Number of jobs to process
		testTimeout   = 3 * time.Minute
	)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	t.Logf("Test configuration: %d schedulers, %d jobs, timeout: %v", numSchedulers, numJobs, testTimeout)

	// Create shared store
	store := NewMockStore()

	// Job execution tracking
	executions := &ConcurrentExecutionTracker{
		counts:     make(map[interface{}]int),
		timestamps: make(map[interface{}][]time.Time),
	}

	// Insert jobs
	t.Log("Creating jobs...")
	startInsert := time.Now()
	jobIDs := make([]interface{}, numJobs)
	now := time.Now()

	for i := 0; i < numJobs; i++ {
		jobID := store.AddJob(&Job{
			SleepUntil: &now,
			Data: map[string]interface{}{
				"jobIndex": i,
				"name":     fmt.Sprintf("job-%06d", i),
			},
		})
		jobIDs[i] = jobID
	}
	t.Logf("Created %d jobs in %v", numJobs, time.Since(startInsert))

	// Start schedulers
	t.Logf("Starting %d concurrent schedulers...", numSchedulers)
	startTime := time.Now()

	var (
		schedulers []*Scheduler
		wg         sync.WaitGroup
		errorCount atomic.Int64
		startMutex sync.Mutex
	)

	// Create all schedulers
	for i := 0; i < numSchedulers; i++ {
		schedulerID := i

		sched, err := New(Config{
			Store: store,
			OnDocument: func(ctx context.Context, job *Job) error {
				// Track execution
				executions.Record(job.ID)
				// Simulate some work
				time.Sleep(1 * time.Millisecond)
				return nil
			},
			OnError: func(ctx context.Context, err error) {
				errorCount.Add(1)
				t.Logf("Scheduler %d error: %v", schedulerID, err)
			},
			NextDelay:    5 * time.Millisecond,   // Fast polling
			LockDuration: 30 * time.Second,       // Reasonable lock time
		})
		if err != nil {
			t.Fatalf("Failed to create scheduler: %v", err)
		}

		schedulers = append(schedulers, sched)
	}

	// Start all schedulers simultaneously for maximum concurrency
	startMutex.Lock()
	for i, sched := range schedulers {
		wg.Add(1)
		go func(idx int, s *Scheduler) {
			defer wg.Done()

			startMutex.Lock() // Wait for all goroutines to be ready
			startMutex.Unlock()

			if err := s.Start(ctx); err != nil {
				t.Errorf("Scheduler %d failed to start: %v", idx, err)
			}
		}(i, sched)
	}

	// Release all schedulers at once
	time.Sleep(100 * time.Millisecond) // Ensure all goroutines are waiting
	startMutex.Unlock()

	// Monitor progress
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// Wait for all jobs to complete or timeout
	monitorCtx, monitorCancel := context.WithCancel(ctx)
	defer monitorCancel()

	go func() {
		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
				completed := executions.TotalExecutions()
				remaining := store.CountAvailableJobs()
				t.Logf("Progress: %d/%d jobs executed, %d remaining, %d errors",
					completed, numJobs, remaining, errorCount.Load())
			}
		}
	}()

	// Wait for all jobs to be processed
	allProcessed := false
	checkInterval := 1 * time.Second
	maxIdleTime := 10 * time.Second
	lastProgress := executions.TotalExecutions()
	idleStart := time.Now()

	for !allProcessed {
		select {
		case <-ctx.Done():
			t.Fatal("Test timeout reached")
		case <-time.After(checkInterval):
			completed := executions.TotalExecutions()
			remaining := store.CountAvailableJobs()

			// Check if we made progress
			if completed != lastProgress {
				lastProgress = completed
				idleStart = time.Now()
			}

			// If all jobs processed or been idle too long
			if remaining == 0 && completed >= numJobs {
				allProcessed = true
			} else if time.Since(idleStart) > maxIdleTime && remaining == 0 {
				// No progress and no jobs remaining
				allProcessed = true
			}
		}
	}

	monitorCancel()
	duration := time.Since(startTime)

	// Stop all schedulers
	t.Log("Stopping schedulers...")
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopCancel()

	for i, sched := range schedulers {
		if err := sched.Stop(stopCtx); err != nil {
			t.Logf("Warning: Scheduler %d stop error: %v", i, err)
		}
	}

	wg.Wait()

	// Analyze results
	separator := strings.Repeat("=", 80)
	t.Log("\n" + separator)
	t.Log("CONCURRENCY TEST RESULTS")
	t.Log(separator)

	stats := executions.GetStats(numJobs, jobIDs)

	t.Logf("\nExecution Statistics:")
	t.Logf("  Total jobs queued:        %d", numJobs)
	t.Logf("  Total executions:         %d", stats.TotalExecutions)
	t.Logf("  Unique jobs executed:     %d", stats.UniqueJobs)
	t.Logf("  Jobs with duplicates:     %d", stats.DuplicateJobs)
	t.Logf("  Total duplicate runs:     %d", stats.TotalDuplicates)
	t.Logf("  Jobs not executed:        %d", stats.MissedJobs)
	t.Logf("  Errors encountered:       %d", errorCount.Load())

	t.Logf("\nPerformance Metrics:")
	t.Logf("  Total duration:           %v", duration)
	t.Logf("  Jobs per second:          %.2f", float64(stats.TotalExecutions)/duration.Seconds())
	t.Logf("  Avg time per job:         %v", duration/time.Duration(stats.TotalExecutions))
	t.Logf("  Concurrent schedulers:    %d", numSchedulers)

	// Show duplicate examples if any
	if len(stats.Duplicates) > 0 {
		t.Logf("\nDuplicate Executions Detected:")
		count := 0
		for jobID, execCount := range stats.Duplicates {
			if count < 10 { // Show first 10
				times := executions.GetTimestamps(jobID)
				t.Logf("  Job %v: executed %d times", jobID, execCount)
				for i, ts := range times {
					t.Logf("    Execution %d: %v", i+1, ts.Format(time.RFC3339Nano))
					if i > 0 {
						// Show time difference between duplicate executions
						diff := ts.Sub(times[i-1])
						t.Logf("      (gap: %v)", diff)
					}
				}
			}
			count++
		}
		if count > 10 {
			t.Logf("  ... and %d more jobs with duplicates", count-10)
		}
	}

	// Show missed jobs if any
	if len(stats.MissedJobIDs) > 0 {
		t.Logf("\nMissed Jobs:")
		for i, jobID := range stats.MissedJobIDs {
			if i < 10 {
				t.Logf("  %v", jobID)
			}
		}
		if len(stats.MissedJobIDs) > 10 {
			t.Logf("  ... and %d more missed jobs", len(stats.MissedJobIDs)-10)
		}
	}

	t.Log(separator)

	// Validate results
	if stats.TotalDuplicates > 0 {
		t.Errorf("FAILED: Found %d duplicate executions across %d jobs",
			stats.TotalDuplicates, stats.DuplicateJobs)
		t.Errorf("This indicates a race condition in the locking mechanism!")
	}

	if stats.MissedJobs > 0 {
		t.Errorf("FAILED: %d jobs were not executed", stats.MissedJobs)
	}

	if stats.UniqueJobs != numJobs {
		t.Errorf("FAILED: Expected %d unique jobs executed, got %d",
			numJobs, stats.UniqueJobs)
	}

	if stats.TotalDuplicates == 0 && stats.MissedJobs == 0 && stats.UniqueJobs == numJobs {
		t.Logf("\n✓ SUCCESS: All %d jobs executed exactly once with no duplicates!", numJobs)
		t.Logf("✓ Locking mechanism is working correctly under high concurrency")
		t.Logf("✓ Processed %.0f jobs/second with %d concurrent schedulers",
			float64(stats.TotalExecutions)/duration.Seconds(), numSchedulers)
	}
}

// ConcurrentExecutionTracker tracks job executions in a thread-safe manner
type ConcurrentExecutionTracker struct {
	mu         sync.RWMutex
	counts     map[interface{}]int
	timestamps map[interface{}][]time.Time
}

func (et *ConcurrentExecutionTracker) Record(jobID interface{}) {
	et.mu.Lock()
	defer et.mu.Unlock()

	et.counts[jobID]++
	et.timestamps[jobID] = append(et.timestamps[jobID], time.Now())
}

func (et *ConcurrentExecutionTracker) TotalExecutions() int {
	et.mu.RLock()
	defer et.mu.RUnlock()

	total := 0
	for _, count := range et.counts {
		total += count
	}
	return total
}

func (et *ConcurrentExecutionTracker) GetTimestamps(jobID interface{}) []time.Time {
	et.mu.RLock()
	defer et.mu.RUnlock()

	return et.timestamps[jobID]
}

type ConcurrentExecutionStats struct {
	TotalExecutions int
	UniqueJobs      int
	DuplicateJobs   int
	TotalDuplicates int
	MissedJobs      int
	Duplicates      map[interface{}]int
	MissedJobIDs    []interface{}
}

func (et *ConcurrentExecutionTracker) GetStats(expectedJobs int, expectedJobIDs []interface{}) ConcurrentExecutionStats {
	et.mu.RLock()
	defer et.mu.RUnlock()

	stats := ConcurrentExecutionStats{
		Duplicates:   make(map[interface{}]int),
		MissedJobIDs: make([]interface{}, 0),
	}

	stats.UniqueJobs = len(et.counts)

	for jobID, count := range et.counts {
		stats.TotalExecutions += count

		if count > 1 {
			stats.DuplicateJobs++
			stats.TotalDuplicates += (count - 1)
			stats.Duplicates[jobID] = count
		}
	}

	// Find missed jobs
	executedSet := make(map[interface{}]bool)
	for jobID := range et.counts {
		executedSet[jobID] = true
	}

	for _, jobID := range expectedJobIDs {
		if !executedSet[jobID] {
			stats.MissedJobIDs = append(stats.MissedJobIDs, jobID)
		}
	}

	stats.MissedJobs = len(stats.MissedJobIDs)

	return stats
}
