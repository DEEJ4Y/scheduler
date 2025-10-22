# Go Scheduler Design Document

## Overview

This document outlines the architecture for a database-agnostic job scheduler in Go, based on the mongodb-cron Node.js package.

## Core Features

### Job Types
1. **One-time jobs**: Execute once at a specified time
2. **Deferred jobs**: Execute once at a future time
3. **Recurring jobs**: Execute repeatedly based on cron expression
4. **Auto-removable jobs**: Delete themselves after completion

### Key Capabilities
- Atomic job locking (prevents race conditions in distributed environments)
- Crash recovery (jobs locked by crashed workers eventually become available)
- Configurable polling and processing delays
- Event-driven architecture with hooks
- Custom field paths for flexibility

## Architecture

### 1. Database Abstraction Layer

The core of the design is the `JobStore` interface that abstracts all database operations:

```go
type JobStore interface {
    // LockNext atomically finds and locks the next available job
    // Returns nil if no job is available
    LockNext(ctx context.Context, lockUntil time.Time) (*Job, error)

    // Update modifies a job's fields
    Update(ctx context.Context, jobID interface{}, updates JobUpdate) error

    // Remove deletes a job from the store
    Remove(ctx context.Context, jobID interface{}) error
}
```

### 2. Core Job Model

```go
type Job struct {
    ID          interface{}            // Job identifier (database-specific)
    SleepUntil  *time.Time            // When the job should execute
    Interval    string                 // Cron expression for recurring jobs
    RepeatUntil *time.Time            // When recurring jobs should stop
    AutoRemove  bool                   // Auto-delete after completion
    Data        map[string]interface{} // Custom job data
}

type JobUpdate struct {
    SleepUntil  *time.Time  // nil means set to null, use pointer to pointer for optional
    AutoRemove  *bool
}
```

### 3. Scheduler Configuration

```go
type Config struct {
    // Required
    Store JobStore

    // Event Handlers
    OnDocument func(ctx context.Context, job *Job) error
    OnStart    func(ctx context.Context) error
    OnStop     func(ctx context.Context) error
    OnIdle     func(ctx context.Context) error
    OnError    func(ctx context.Context, err error) error

    // Timing Configuration
    NextDelay      time.Duration // Wait before processing next job (default: 0)
    ReprocessDelay time.Duration // Wait before reprocessing recurring job (default: 0)
    IdleDelay      time.Duration // Wait when no jobs available (default: 0)
    LockDuration   time.Duration // How long a job is locked (default: 10 minutes)
}
```

### 4. Core Scheduler

```go
type Scheduler struct {
    store      JobStore
    config     Config
    running    atomic.Bool
    processing atomic.Bool
    idle       atomic.Bool
    stopCh     chan struct{}
    wg         sync.WaitGroup
}

func New(config Config) (*Scheduler, error)
func (s *Scheduler) Start(ctx context.Context) error
func (s *Scheduler) Stop(ctx context.Context) error
func (s *Scheduler) IsRunning() bool
func (s *Scheduler) IsProcessing() bool
func (s *Scheduler) IsIdle() bool
```

## MongoDB Implementation

### Store Configuration

```go
type MongoConfig struct {
    Collection  *mongo.Collection

    // Custom field paths (optional)
    SleepUntilField  string // default: "sleepUntil"
    IntervalField    string // default: "interval"
    RepeatUntilField string // default: "repeatUntil"
    AutoRemoveField  string // default: "autoRemove"

    // Additional filter condition
    Condition bson.M
}

type MongoStore struct {
    config MongoConfig
}

func NewMongoStore(config MongoConfig) *MongoStore
```

### MongoDB LockNext Implementation

Uses `findOneAndUpdate` with atomic operations:

```javascript
db.collection.findOneAndUpdate(
    {
        $and: [
            { sleepUntil: { $exists: true, $ne: null } },
            { sleepUntil: { $not: { $gt: currentDate } } },
            condition  // optional additional filter
        ]
    },
    {
        $set: { sleepUntil: lockUntilDate }
    },
    {
        returnDocument: "before"  // return original to calculate next start
    }
)
```

## Processing Flow

1. **Tick Loop**:
   - Wait for `NextDelay`
   - Call `LockNext()` to atomically get next job
   - If no job found: trigger `OnIdle` (once), sleep `IdleDelay`
   - If job found: process it

2. **Job Processing**:
   - Call `OnDocument` handler
   - Calculate next execution time (for recurring jobs)
   - Reschedule or remove job based on result

3. **Rescheduling**:
   - If job has `interval`: calculate next execution time
   - If next time exists: update `sleepUntil`
   - If no next time and `autoRemove`: delete job
   - If no next time: set `sleepUntil` to null (completed)

## Cron Expression Handling

Use `github.com/robfig/cron/v3` for cron parsing:

```go
func calculateNextStart(job *Job, reprocessDelay time.Duration) (*time.Time, error) {
    if job.Interval == "" {
        return nil, nil // not recurring
    }

    schedule, err := cron.ParseStandard(job.Interval)
    if err != nil {
        return nil, err
    }

    available := time.Now()
    if job.SleepUntil != nil {
        available = *job.SleepUntil
    }

    next := schedule.Next(available.Add(reprocessDelay))

    // Check if past repeatUntil
    if job.RepeatUntil != nil && next.After(*job.RepeatUntil) {
        return nil, nil
    }

    // Don't process old jobs multiple times
    now := time.Now()
    if next.Before(now) {
        next = now
    }

    return &next, nil
}
```

## Error Handling

- All methods return errors (no panics)
- Context cancellation support throughout
- Graceful shutdown with proper cleanup
- Error callbacks for observability

## Concurrency Safety

- Atomic operations for state flags
- Proper locking in MongoDB using `findOneAndUpdate`
- Context-based cancellation
- WaitGroup for graceful shutdown

## Package Structure

```
scheduler/
├── README.md
├── DESIGN.md
├── go.mod
├── go.sum
├── scheduler.go          # Core scheduler implementation
├── job.go               # Job model and types
├── store.go             # JobStore interface
├── cron.go              # Cron expression utilities
├── mongodb/
│   ├── store.go         # MongoDB implementation
│   ├── store_test.go
│   └── options.go       # MongoDB-specific options
├── examples/
│   ├── basic/
│   │   └── main.go
│   └── mongodb/
│       └── main.go
└── internal/
    └── testutil/        # Test helpers
```

## Dependencies

- `go.mongodb.org/mongo-driver/mongo` - MongoDB driver
- `github.com/robfig/cron/v3` - Cron expression parsing
- Standard library: `context`, `sync`, `time`, etc.

## Testing Strategy

1. **Unit Tests**: Test core logic with mock store
2. **Integration Tests**: Test MongoDB implementation with real MongoDB
3. **Concurrent Tests**: Verify behavior under concurrent access
4. **Recovery Tests**: Simulate crashes and verify recovery

## Future Extensibility

The interface design allows for:
- PostgreSQL implementation (using advisory locks)
- Redis implementation (using SETNX/EXPIRE)
- SQL databases (using SELECT FOR UPDATE)
- In-memory implementation (for testing)

Each implementation can provide database-specific optimizations while maintaining the same core scheduler logic.
