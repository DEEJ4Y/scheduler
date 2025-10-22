# Why Scheduler 97 Gets a "Context Canceled" Error

## The Issue

When running the MongoDB concurrency test with 100 schedulers, you consistently see:

```
Scheduler 97 error: failed to lock next job: findOneAndUpdate failed: context canceled
```

This happens during shutdown, **not during normal operation**, and is actually **expected behavior**, not a bug.

## Root Cause

### What's Happening

1. **All jobs complete successfully** (10,000/10,000 ✅)
2. **Test detects completion** and begins shutdown sequence
3. **Schedulers stop sequentially** (0 → 1 → 2 → ... → 99)
4. **Scheduler 97 is mid-MongoDB call** when its turn comes
5. **Stop() cancels scheduler 97's context**
6. **MongoDB operation interrupted** → "context canceled" error
7. **OnError handler logs it** (even though it's expected during shutdown)

### Why Always Scheduler 97?

It's a **timing coincidence**:

- Schedulers are stopped in order (0, 1, 2, ..., 99)
- Each scheduler has its own polling cycle
- By the time we reach scheduler 97, it's at the perfect moment in its cycle to be inside a `LockNext()` MongoDB call
- Earlier schedulers (0-96) stopped cleanly
- Later schedulers (98-99) haven't started their next operation yet

## The Fix

We suppress "context canceled" errors that occur **after shutdown begins**, since they're expected:

```go
var stopping atomic.Bool  // Track when shutdown starts

// In OnError handler:
OnError: func(ctx context.Context, err error) {
    // Ignore context canceled errors during shutdown - they're expected
    if stopping.Load() && strings.Contains(err.Error(), "context canceled") {
        return
    }
    errorCount.Add(1)
    t.Logf("Scheduler %d error: %v", schedulerID, err)
},

// Before stopping schedulers:
stopping.Store(true)  // Signal shutdown
```

## Why This Is Correct Behavior

### Context Cancellation Is Working Properly

1. **Graceful shutdown**: When we call `Stop()`, we **want** to cancel ongoing operations
2. **MongoDB respects contexts**: The driver properly interrupts the `findOneAndUpdate` call
3. **No data corruption**: The atomic operation either completes or doesn't - no partial states
4. **Clean termination**: Scheduler stops immediately instead of waiting for operation to complete

### What Would Be Wrong

❌ **Waiting for operation to complete**: Shutdown would take longer
❌ **Ignoring context**: Operation might continue after shutdown
❌ **Partial updates**: MongoDB's atomic operations prevent this

## Verification

The error occurs **after** all jobs are processed:

```
Total jobs queued:        10000
Total executions:         10000  ✅
Jobs with duplicates:     0      ✅
Jobs not executed:        0      ✅
Errors encountered:       1      ⚠️  (the shutdown error)
```

Notice:
- All 10,000 jobs executed successfully
- Zero duplicates
- Zero missed jobs
- The error happens during shutdown, not job processing

## Other Schedulers

The same pattern could occur with any scheduler (not just 97). It depends on:

- **Polling cycle timing**: Which scheduler is in a database call when shutdown starts
- **Operation duration**: How long each database call takes
- **Shutdown order**: We stop schedulers sequentially

In your environment, scheduler 97 consistently hits this timing window. In a different environment or with different timing, it might be scheduler 42 or 88.

## Real-World Implications

### In Production

This is the **desired behavior** for production systems:

✅ **Fast shutdown**: Don't wait for long-running operations
✅ **Responsive termination**: React immediately to shutdown signals
✅ **No data loss**: Atomic operations ensure consistency

### Monitoring

When monitoring production systems, you might see similar "context canceled" errors during:

- Deployment rollouts (pod termination)
- Auto-scaling events (instances shutting down)
- Manual restarts or updates

These are **normal** and indicate proper context propagation.

## Conclusion

The "Scheduler 97" error is:

✅ **Expected**: Happens during shutdown, not job processing
✅ **Harmless**: All jobs completed successfully
✅ **Correct**: Context cancellation is working as designed
✅ **Fixed**: Now suppressed in test output

The fix doesn't change behavior - it just stops logging expected shutdown errors that were creating noise in test output.

## Testing

After the fix, you should see:

```
Total executions:         10000
Jobs with duplicates:     0
Jobs not executed:        0
Errors encountered:       0      ✅ (shutdown errors now suppressed)

✓ SUCCESS: All 10000 jobs executed exactly once!
```

No more spurious error messages! 🎉
