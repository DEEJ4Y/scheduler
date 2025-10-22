// +build !race

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

// TestConcurrentSchedulersLarge is a stress test with 100 schedulers and 10,000 jobs.
// Skipped in race detector mode as it's intentionally creating high concurrency.
func TestConcurrentSchedulersLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping large concurrency test in short mode")
	}

	const (
		numSchedulers = 100   // High concurrency
		numJobs       = 10000 // Large job count
		testTimeout   = 5 * time.Minute
	)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	t.Logf("LARGE STRESS TEST: %d schedulers, %d jobs, timeout: %v", numSchedulers, numJobs, testTimeout)

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
		stopping   atomic.Bool // Flag to ignore expected shutdown errors
	)

	// Create all schedulers
	for i := 0; i < numSchedulers; i++ {
		schedulerID := i

		sched, err := New(Config{
			Store: store,
			OnDocument: func(ctx context.Context, job *Job) error {
				// Track execution
				executions.Record(job.ID)
				// Simulate very light work
				time.Sleep(100 * time.Microsecond)
				return nil
			},
			OnError: func(ctx context.Context, err error) {
				// Ignore context canceled errors during shutdown - they're expected
				if stopping.Load() && strings.Contains(err.Error(), "context canceled") {
					return
				}
				errorCount.Add(1)
				// Only log first few errors to avoid spam
				if errorCount.Load() <= 5 {
					t.Logf("Scheduler %d error: %v", schedulerID, err)
				}
			},
			NextDelay:    2 * time.Millisecond, // Very fast polling for stress test
			LockDuration: 30 * time.Second,
		})
		if err != nil {
			t.Fatalf("Failed to create scheduler: %v", err)
		}

		schedulers = append(schedulers, sched)
	}

	// Start all schedulers simultaneously for maximum race condition potential
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
	time.Sleep(200 * time.Millisecond)
	t.Log("Releasing all schedulers simultaneously...")
	startMutex.Unlock()

	// Monitor progress
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// Monitor in background
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
				t.Logf("Progress: %d/%d jobs executed (%.1f%%), %d remaining, %d errors",
					completed, numJobs, float64(completed)/float64(numJobs)*100,
					remaining, errorCount.Load())
			}
		}
	}()

	// Wait for completion
	allProcessed := false
	checkInterval := 2 * time.Second
	maxIdleTime := 15 * time.Second
	lastProgress := executions.TotalExecutions()
	idleStart := time.Now()

	for !allProcessed {
		select {
		case <-ctx.Done():
			t.Fatal("Test timeout reached")
		case <-time.After(checkInterval):
			completed := executions.TotalExecutions()
			remaining := store.CountAvailableJobs()

			if completed != lastProgress {
				lastProgress = completed
				idleStart = time.Now()
			}

			if remaining == 0 && completed >= numJobs {
				allProcessed = true
			} else if time.Since(idleStart) > maxIdleTime && remaining == 0 {
				allProcessed = true
			}
		}
	}

	monitorCancel()
	duration := time.Since(startTime)

	// Stop all schedulers
	t.Log("Stopping all schedulers...")
	stopping.Store(true) // Signal shutdown - context canceled errors are expected now
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopCancel()

	for _, sched := range schedulers {
		go sched.Stop(stopCtx) // Stop in parallel for speed
	}

	wg.Wait()

	// Analyze results
	separator := strings.Repeat("=", 80)
	t.Log("\n" + separator)
	t.Log("LARGE STRESS TEST RESULTS")
	t.Log(separator)

	stats := executions.GetStats(numJobs, jobIDs)

	t.Logf("\nTest Configuration:")
	t.Logf("  Concurrent schedulers:    %d", numSchedulers)
	t.Logf("  Total jobs queued:        %d", numJobs)

	t.Logf("\nExecution Statistics:")
	t.Logf("  Total executions:         %d", stats.TotalExecutions)
	t.Logf("  Unique jobs executed:     %d", stats.UniqueJobs)
	t.Logf("  Jobs with duplicates:     %d", stats.DuplicateJobs)
	t.Logf("  Total duplicate runs:     %d", stats.TotalDuplicates)
	t.Logf("  Jobs not executed:        %d", stats.MissedJobs)
	t.Logf("  Errors encountered:       %d", errorCount.Load())

	if stats.TotalDuplicates > 0 {
		duplicateRate := float64(stats.TotalDuplicates) / float64(stats.TotalExecutions) * 100
		t.Logf("  Duplicate rate:           %.4f%%", duplicateRate)
	}

	t.Logf("\nPerformance Metrics:")
	t.Logf("  Total duration:           %v", duration)
	t.Logf("  Jobs per second:          %.2f", float64(stats.TotalExecutions)/duration.Seconds())
	t.Logf("  Avg time per job:         %v", duration/time.Duration(stats.TotalExecutions))
	t.Logf("  Throughput per scheduler: %.2f jobs/sec", float64(stats.TotalExecutions)/duration.Seconds()/float64(numSchedulers))

	// Show duplicate details if any
	if len(stats.Duplicates) > 0 {
		t.Logf("\n⚠ WARNING: Duplicate Executions Detected!")
		t.Logf("Showing first 5 examples:")
		count := 0
		for jobID, execCount := range stats.Duplicates {
			if count < 5 {
				times := executions.GetTimestamps(jobID)
				t.Logf("\n  Job %v: executed %d times", jobID, execCount)
				for i, ts := range times {
					t.Logf("    Execution %d: %v", i+1, ts.Format("15:04:05.000000"))
					if i > 0 {
						diff := ts.Sub(times[i-1])
						t.Logf("      Time gap: %v", diff)
					}
				}
			}
			count++
		}
		if count > 5 {
			t.Logf("\n  ... and %d more jobs with duplicates", count-5)
		}
	}

	t.Log(separator)

	// Final validation
	if stats.TotalDuplicates > 0 {
		t.Errorf("❌ FAILED: Found %d duplicate executions across %d jobs",
			stats.TotalDuplicates, stats.DuplicateJobs)
		t.Errorf("This indicates a race condition in the locking mechanism!")
	}

	if stats.MissedJobs > 0 {
		t.Errorf("❌ FAILED: %d jobs were not executed", stats.MissedJobs)
	}

	if stats.UniqueJobs != numJobs {
		t.Errorf("❌ FAILED: Expected %d unique jobs executed, got %d",
			numJobs, stats.UniqueJobs)
	}

	if stats.TotalDuplicates == 0 && stats.MissedJobs == 0 && stats.UniqueJobs == numJobs {
		t.Logf("\n✅ SUCCESS: All %d jobs executed exactly once!", numJobs)
		t.Logf("✅ NO DUPLICATES found with %d concurrent schedulers!", numSchedulers)
		t.Logf("✅ Distributed locking mechanism PASSED stress test!")
		t.Logf("✅ Achieved %.0f jobs/second throughput", float64(stats.TotalExecutions)/duration.Seconds())
	}
}
