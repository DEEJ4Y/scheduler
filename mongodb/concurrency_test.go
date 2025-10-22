package mongodb

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DEEJ4Y/scheduler"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// TestDistributedLocking validates that jobs execute exactly once under high concurrency.
// This test simulates 100 concurrent scheduler instances processing 10,000 jobs.
func TestDistributedLocking(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrency test in short mode")
	}

	const (
		numSchedulers = 100   // Number of concurrent scheduler instances
		numJobs       = 10000 // Number of jobs to process
		testTimeout   = 5 * time.Minute
	)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	// Connect to MongoDB
	mongoURI := "mongodb://localhost:27017"
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		t.Skipf("Skipping test: MongoDB not available: %v", err)
	}
	defer client.Disconnect(ctx)

	// Verify connection
	if err := client.Ping(ctx, nil); err != nil {
		t.Skipf("Skipping test: Cannot ping MongoDB: %v", err)
	}

	// Use a unique database for this test
	dbName := fmt.Sprintf("scheduler_concurrency_test_%d", time.Now().Unix())
	db := client.Database(dbName)
	defer db.Drop(context.Background())

	collection := db.Collection("jobs")

	// Create index for performance
	indexModel := mongo.IndexModel{
		Keys:    bson.D{{Key: "sleepUntil", Value: 1}},
		Options: options.Index().SetSparse(true),
	}
	_, err = collection.Indexes().CreateOne(ctx, indexModel)
	if err != nil {
		t.Fatalf("Failed to create index: %v", err)
	}

	t.Logf("Test configuration: %d schedulers, %d jobs, timeout: %v", numSchedulers, numJobs, testTimeout)

	// Job execution tracking
	executions := &ExecutionTracker{
		counts:     make(map[string]int),
		timestamps: make(map[string][]time.Time),
	}

	// Insert jobs
	t.Log("Inserting jobs...")
	startInsert := time.Now()
	if err := insertJobs(ctx, collection, numJobs); err != nil {
		t.Fatalf("Failed to insert jobs: %v", err)
	}
	t.Logf("Inserted %d jobs in %v", numJobs, time.Since(startInsert))

	// Start schedulers
	t.Logf("Starting %d concurrent schedulers...", numSchedulers)
	startTime := time.Now()

	var (
		schedulers []*scheduler.Scheduler
		wg         sync.WaitGroup
		errorCount atomic.Int64
		startMutex sync.Mutex
		stopping   atomic.Bool // Flag to track when shutdown begins
	)

	// Create and start all schedulers
	for i := 0; i < numSchedulers; i++ {
		schedulerID := i

		store, err := NewStore(Config{
			Collection: collection,
		})
		if err != nil {
			t.Fatalf("Failed to create store: %v", err)
		}

		sched, err := scheduler.New(scheduler.Config{
			Store: store,
			OnDocument: func(ctx context.Context, job *scheduler.Job) error {
				// Track execution
				jobID := getJobID(job)
				executions.Record(jobID)
				return nil
			},
			OnError: func(ctx context.Context, err error) {
				// Ignore context canceled errors during shutdown - they're expected
				if stopping.Load() && strings.Contains(err.Error(), "context canceled") {
					return
				}
				errorCount.Add(1)
				t.Logf("Scheduler %d error: %v", schedulerID, err)
			},
			NextDelay:    10 * time.Millisecond,  // Fast polling
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
		go func(idx int, s *scheduler.Scheduler) {
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
				remaining, _ := countRemainingJobs(context.Background(), collection)
				t.Logf("Progress: %d/%d jobs executed, %d remaining, %d errors",
					completed, numJobs, remaining, errorCount.Load())
			}
		}
	}()

	// Wait for all jobs to be processed
	allProcessed := false
	checkInterval := 2 * time.Second
	maxIdleTime := 10 * time.Second
	lastProgress := executions.TotalExecutions()
	idleStart := time.Now()

	for !allProcessed {
		select {
		case <-ctx.Done():
			t.Fatal("Test timeout reached")
		case <-time.After(checkInterval):
			completed := executions.TotalExecutions()
			remaining, _ := countRemainingJobs(context.Background(), collection)

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
	// Set stopping flag to ignore context.Canceled errors during shutdown.
	// These occur when a scheduler is mid-MongoDB call when its context gets canceled.
	// This is expected behavior and not an error - the schedulers stop cleanly.
	stopping.Store(true)
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
	t.Log("TEST RESULTS")
	t.Log(separator)

	stats := executions.GetStats()

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

	// Show duplicate examples if any
	if len(stats.Duplicates) > 0 {
		t.Logf("\nDuplicate Executions Detected:")
		count := 0
		for jobID, execCount := range stats.Duplicates {
			if count < 10 { // Show first 10
				times := executions.GetTimestamps(jobID)
				t.Logf("  Job %s: executed %d times", jobID, execCount)
				for i, ts := range times {
					t.Logf("    Execution %d: %v", i+1, ts.Format(time.RFC3339Nano))
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
				t.Logf("  %s", jobID)
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
		t.Logf("✓ Distributed locking mechanism is working correctly under high concurrency")
	}
}

// ExecutionTracker tracks job executions in a thread-safe manner
type ExecutionTracker struct {
	mu         sync.RWMutex
	counts     map[string]int
	timestamps map[string][]time.Time
}

func (et *ExecutionTracker) Record(jobID string) {
	et.mu.Lock()
	defer et.mu.Unlock()

	et.counts[jobID]++
	et.timestamps[jobID] = append(et.timestamps[jobID], time.Now())
}

func (et *ExecutionTracker) TotalExecutions() int {
	et.mu.RLock()
	defer et.mu.RUnlock()

	total := 0
	for _, count := range et.counts {
		total += count
	}
	return total
}

func (et *ExecutionTracker) GetTimestamps(jobID string) []time.Time {
	et.mu.RLock()
	defer et.mu.RUnlock()

	return et.timestamps[jobID]
}

type ExecutionStats struct {
	TotalExecutions int
	UniqueJobs      int
	DuplicateJobs   int
	TotalDuplicates int
	MissedJobs      int
	Duplicates      map[string]int
	MissedJobIDs    []string
}

func (et *ExecutionTracker) GetStats() ExecutionStats {
	et.mu.RLock()
	defer et.mu.RUnlock()

	stats := ExecutionStats{
		Duplicates:   make(map[string]int),
		MissedJobIDs: make([]string, 0),
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

	return stats
}

// Helper functions

func insertJobs(ctx context.Context, collection *mongo.Collection, count int) error {
	now := time.Now()

	// Insert in batches for better performance
	batchSize := 1000
	for i := 0; i < count; i += batchSize {
		batch := make([]interface{}, 0, batchSize)
		end := i + batchSize
		if end > count {
			end = count
		}

		for j := i; j < end; j++ {
			batch = append(batch, bson.M{
				"jobID":      fmt.Sprintf("job-%06d", j),
				"sleepUntil": now,
				"data":       fmt.Sprintf("test-job-%d", j),
			})
		}

		if _, err := collection.InsertMany(ctx, batch); err != nil {
			return fmt.Errorf("batch insert failed: %w", err)
		}
	}

	return nil
}

func countRemainingJobs(ctx context.Context, collection *mongo.Collection) (int64, error) {
	count, err := collection.CountDocuments(ctx, bson.M{
		"sleepUntil": bson.M{"$ne": nil},
	})
	return count, err
}

func getJobID(job *scheduler.Job) string {
	if jobID, ok := job.Data["jobID"].(string); ok {
		return jobID
	}
	return fmt.Sprintf("%v", job.ID)
}
