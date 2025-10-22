# Node.js mongodb-cron Concurrency Test

## Overview

A comprehensive concurrency test for the original mongodb-cron Node.js package to compare performance with the Go implementation.

## Test Setup

The test is designed to mirror the Go implementation's concurrency test as closely as possible for fair comparison:

### Test Configuration

- **Concurrent Cron Instances**: 100
- **Total Jobs**: 10,000
- **Job Executor**: Minimal (just tracking, no heavy work)
- **Polling Interval**: 10ms (matches Go test)
- **Lock Duration**: 30 seconds (matches Go test)

### Test Parameters

All parameters match the Go implementation to ensure fair comparison:

```typescript
{
  nextDelay: 10,        // 10ms polling interval
  reprocessDelay: 0,    // No delay for one-time jobs
  idleDelay: 0,         // Check immediately when idle
  lockDuration: 30000,  // 30 second lock duration
}
```

## Running the Test

### Prerequisites

1. MongoDB running on localhost:27017
2. Node.js installed
3. Dependencies installed: `npm install` (in mongodb-cron directory)

### Execute Test

```bash
cd mongodb-cron
npm run concurrency-test
```

### Expected Output

```
================================================================================
NODEJS MONGODB-CRON CONCURRENCY TEST
================================================================================
Configuration: 100 crons, 10000 jobs

Creating index...
Inserting 10000 jobs...
Inserted 10000 jobs in XXXms

Starting 100 concurrent MongoCron instances...
Progress: XXXX executions, XXXX unique jobs
...
Stopping all crons...

================================================================================
TEST RESULTS
================================================================================

Test Configuration:
  Concurrent crons:         100
  Total jobs queued:        10000

Execution Statistics:
  Total executions:         10000
  Unique jobs executed:     10000
  Jobs with duplicates:     0
  Total duplicate runs:     0
  Jobs not executed:        0
  Errors encountered:       0

Performance Metrics:
  Total duration:           X.XXXs
  Jobs per second:          XXXX.XX
  Avg time per job:         XXXus
  Throughput per cron:      XX.XX jobs/sec

================================================================================
SUCCESS: All 10000 jobs executed exactly once!
NO DUPLICATES found with 100 concurrent crons!
Distributed locking mechanism PASSED stress test!
Achieved XXXX jobs/second throughput
```

## What the Test Validates

### Correctness

- ✅ **Zero Duplicate Executions**: Each job processed exactly once
- ✅ **Zero Missed Jobs**: All queued jobs are processed
- ✅ **Atomic Locking**: MongoDB's `findOneAndUpdate` prevents race conditions

### Performance

- 📊 **Throughput**: Jobs processed per second
- 📊 **Latency**: Average microseconds per job
- 📊 **Scalability**: Performance with 100 concurrent instances

### Stress Testing

- 🔥 **Simultaneous Start**: All 100 crons start at once (worst-case race conditions)
- 🔥 **High Contention**: Fast polling (10ms) creates maximum lock contention
- 🔥 **Large Scale**: 10,000 jobs ensures sustained load

## How It Works

### Test Flow

1. **Setup**
   - Create unique test database
   - Create sparse index on `sleepUntil`
   - Insert 10,000 jobs in batches

2. **Execution**
   - Start 100 MongoCron instances simultaneously
   - Each instance polls for available jobs
   - Track all job executions with timestamps
   - Monitor progress every 5 seconds

3. **Monitoring**
   - Wait for all jobs to complete or idle timeout
   - Stop all cron instances
   - Analyze results for duplicates and missed jobs

4. **Cleanup**
   - Drop test database
   - Close MongoDB connection

### Job Executor

The `onDocument` handler is intentionally minimal to match the Go test:

```typescript
onDocument: async (doc) => {
  const jobID = doc.jobID || ('unknown-' + doc._id);
  recordExecution(tracker, jobId);
}
```

This ensures we're testing the locking mechanism, not job processing overhead.

## Comparison with Go Implementation

### Similarities

Both tests use identical configuration:

| Parameter | Value |
|-----------|-------|
| Concurrent Workers | 100 |
| Total Jobs | 10,000 |
| Polling Interval | 10ms |
| Lock Duration | 30 seconds |
| Job Executor | Minimal (tracking only) |

### Differences

| Aspect | Node.js (mongodb-cron) | Go (scheduler) |
|--------|----------------------|-----------------|
| Concurrency Model | Event loop + callbacks | Goroutines |
| Database Driver | mongodb@6.3.0 | mongo-driver@1.17.1 |
| Language Runtime | Node.js/V8 | Go runtime |
| Memory Model | GC (V8) | GC (Go) |

## Expected Results

Based on the test design, we expect:

### Correctness (Both Should Pass)

- ✅ Zero duplicate executions
- ✅ Zero missed jobs
- ✅ All 10,000 jobs processed exactly once

### Performance (Varies by Implementation)

The performance comparison will show:

- **Throughput**: Which implementation processes more jobs/second
- **Latency**: Which has lower per-job overhead
- **Scalability**: How each handles 100 concurrent workers

## Files

- **Test**: `mongodb-cron/src/scripts/concurrency-test.ts`
- **Package Script**: `npm run concurrency-test`
- **Results**: To be added after running

## Notes

### Why This Test Matters

1. **Validates Original**: Proves mongodb-cron's locking mechanism works correctly
2. **Fair Comparison**: Same test parameters as Go implementation
3. **Real-World Scenario**: 100 workers is a realistic deployment scale
4. **Stress Test**: Identifies any race conditions or edge cases

### Interpreting Results

- **Success Criteria**: Zero duplicates, zero missed jobs
- **Performance**: Raw throughput numbers (Go will likely be faster due to compiled code)
- **Locking Correctness**: Both should have perfect locking (zero duplicates)

The key insight is that **correctness** (zero duplicates) is more important than **speed** for a distributed locking system. Both implementations should achieve perfect correctness.
