# Go Scheduler

> Database-agnostic job scheduler for Go, inspired by [mongodb-cron](https://github.com/xpepermint/mongodb-cron)

A flexible, database-agnostic job scheduling library for Go that can turn any database into a job queue or crontab-like system. Built with a clean architecture that separates core scheduling logic from database-specific implementations.

## Features

- **Database Agnostic**: Clean interface allows any database to be used (MongoDB, PostgreSQL, Redis, etc.)
- **Multiple Job Types**:
  - One-time jobs
  - Deferred jobs (schedule for future execution)
  - Recurring jobs (cron expressions)
  - Auto-removable jobs
- **Distributed Safe**: Uses atomic locking to prevent race conditions
- **Crash Recovery**: Jobs locked by crashed workers automatically become available
- **Event-Driven**: Hooks for job processing, errors, idle states, etc.
- **Concurrent-Safe**: Built with Go concurrency primitives
- **Context Support**: Proper context handling for cancellation and timeouts

## Installation

```bash
go get github.com/DEEJ4Y/scheduler
```

For MongoDB support:
```bash
go get github.com/DEEJ4Y/scheduler/mongodb
```

## Quick Start

### Basic Example (In-Memory)

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/DEEJ4Y/scheduler"
)

func main() {
    ctx := context.Background()

    // Create a scheduler with an in-memory store
    // (see examples/basic for full implementation)
    store := NewInMemoryStore() // your implementation

    sched, err := scheduler.New(scheduler.Config{
        Store: store,
        OnDocument: func(ctx context.Context, job *scheduler.Job) error {
            fmt.Printf("Processing job: %v\n", job.Data)
            return nil
        },
        NextDelay:      1 * time.Second,
        LockDuration:   10 * time.Minute,
    })
    if err != nil {
        panic(err)
    }

    // Start processing
    sched.Start(ctx)
    defer sched.Stop(context.Background())

    // Add jobs to your store...
}
```

### MongoDB Example

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/DEEJ4Y/scheduler"
    "github.com/DEEJ4Y/scheduler/mongodb"
    "go.mongodb.org/mongo-driver/bson"
    "go.mongodb.org/mongo-driver/mongo"
    "go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
    ctx := context.Background()

    // Connect to MongoDB
    client, err := mongo.Connect(ctx, options.Client().ApplyURI("mongodb://localhost:27017"))
    if err != nil {
        panic(err)
    }
    defer client.Disconnect(ctx)

    collection := client.Database("myapp").Collection("jobs")

    // Create MongoDB store
    store, err := mongodb.NewStore(mongodb.Config{
        Collection: collection,
    })
    if err != nil {
        panic(err)
    }

    // Create scheduler
    sched, err := scheduler.New(scheduler.Config{
        Store: store,
        OnDocument: func(ctx context.Context, job *scheduler.Job) error {
            fmt.Printf("Processing: %v\n", job.Data["name"])
            // Do your work here
            return nil
        },
        OnError: func(ctx context.Context, err error) {
            fmt.Printf("Error: %v\n", err)
        },
        NextDelay:      1 * time.Second,
        IdleDelay:      10 * time.Second,
        LockDuration:   10 * time.Minute,
    })
    if err != nil {
        panic(err)
    }

    // Start scheduler
    sched.Start(ctx)
    defer sched.Stop(context.Background())

    // Create jobs
    now := time.Now()
    collection.InsertOne(ctx, bson.M{
        "name":       "My Job",
        "sleepUntil": now,
        "data":       "custom payload",
    })
}
```

## Job Types

### One-Time Job

Execute once immediately or at a specific time:

```go
// Immediate execution
collection.InsertOne(ctx, bson.M{
    "sleepUntil": time.Now(),
    "data": "my job data",
})

// Deferred execution
collection.InsertOne(ctx, bson.M{
    "sleepUntil": time.Now().Add(1 * time.Hour),
    "data": "process later",
})
```

### Recurring Job

Execute repeatedly based on a cron expression:

```go
collection.InsertOne(ctx, bson.M{
    "sleepUntil": time.Now(),
    "interval":   "*/5 * * * * *", // every 5 seconds
    "data":       "recurring task",
})
```

Cron format (6 fields):
```
* * * * * *
┬ ┬ ┬ ┬ ┬ ┬
│ │ │ │ │ └── day of week (0 - 7) (0 or 7 is Sunday)
│ │ │ │ └──── month (1 - 12)
│ │ │ └────── day of month (1 - 31)
│ │ └──────── hour (0 - 23)
│ └────────── minute (0 - 59)
└──────────── second (0 - 59)
```

### Recurring Job with Expiration

Stop repeating after a certain time:

```go
collection.InsertOne(ctx, bson.M{
    "sleepUntil":  time.Now(),
    "interval":    "0 0 * * * *", // every hour
    "repeatUntil": time.Now().Add(24 * time.Hour), // stop after 24 hours
})
```

### Auto-Remove Job

Automatically delete after completion:

```go
collection.InsertOne(ctx, bson.M{
    "sleepUntil": time.Now(),
    "autoRemove": true,
})
```

## Configuration

### Scheduler Config

```go
type Config struct {
    // Required
    Store JobStore  // Database implementation

    // Event Handlers (all optional)
    OnDocument func(ctx context.Context, job *Job) error  // Job processing
    OnStart    func(ctx context.Context) error            // Scheduler start
    OnStop     func(ctx context.Context) error            // Scheduler stop
    OnIdle     func(ctx context.Context) error            // No jobs available
    OnError    func(ctx context.Context, err error)       // Error occurred

    // Timing Configuration
    NextDelay      time.Duration  // Wait before next job (default: 0)
    ReprocessDelay time.Duration  // Wait before reprocessing recurring job (default: 0)
    IdleDelay      time.Duration  // Wait when no jobs available (default: 0)
    LockDuration   time.Duration  // How long jobs are locked (default: 10 minutes)
}
```

### MongoDB Config

```go
type Config struct {
    Collection *mongo.Collection  // Required

    // Custom field names (optional)
    SleepUntilField  string  // default: "sleepUntil"
    IntervalField    string  // default: "interval"
    RepeatUntilField string  // default: "repeatUntil"
    AutoRemoveField  string  // default: "autoRemove"

    // Additional filter condition (optional)
    Condition bson.M  // e.g., bson.M{"type": "email"}
}
```

## Architecture

### Core Components

1. **JobStore Interface**: Abstracts database operations
   - `LockNext()`: Atomically locks the next available job
   - `Update()`: Updates job fields
   - `Remove()`: Deletes a job

2. **Scheduler**: Core scheduling logic (database-agnostic)
   - Polling loop for job processing
   - Event-driven hooks
   - Graceful shutdown

3. **Database Implementations**:
   - MongoDB (included)
   - PostgreSQL, Redis, etc. (can be implemented by users)

### How It Works

1. Scheduler continuously polls for available jobs
2. Jobs with `sleepUntil <= now` are candidates for processing
3. `LockNext()` atomically locks a job by updating `sleepUntil` to `now + lockDuration`
4. Job is processed by calling `OnDocument` handler
5. After processing:
   - One-time jobs: `sleepUntil` set to `nil` (completed)
   - Recurring jobs: `sleepUntil` set to next execution time
   - Auto-remove jobs: deleted from database

### Crash Recovery

If a worker crashes while processing a job:
- Job remains locked until `lockDuration` expires
- After expiration, job becomes available for another worker
- Ensures jobs are eventually processed even after crashes

## Implementing Your Own Store

Implement the `JobStore` interface for your database:

```go
type JobStore interface {
    LockNext(ctx context.Context, lockUntil time.Time) (*Job, error)
    Update(ctx context.Context, jobID interface{}, updates JobUpdate) error
    Remove(ctx context.Context, jobID interface{}) error
}
```

See `mongodb/store.go` for a complete example.

Key requirements:
- `LockNext` must be atomic to prevent race conditions
- Return the job data BEFORE locking (original `sleepUntil` value)
- Handle `nil` values correctly for `sleepUntil`

## Performance

### MongoDB Indexes

For better performance with MongoDB, create an index on `sleepUntil`:

```go
indexModel := mongo.IndexModel{
    Keys: bson.D{{Key: "sleepUntil", Value: 1}},
    Options: options.Index().SetSparse(true),
}
collection.Indexes().CreateOne(ctx, indexModel)
```

Adjust the index if using custom field paths or conditions.

### Tuning

- **NextDelay**: Reduce CPU usage by adding delay between job checks
- **IdleDelay**: When no jobs are available, wait longer before checking again
- **LockDuration**: Balance between quick recovery and avoiding duplicate processing
- **ReprocessDelay**: For recurring jobs, add delay before next execution

## Best Practices

1. **Idempotent Jobs**: Design jobs to be safely re-executed
   - A crashed worker may process a job twice
   - Use unique transaction IDs or database constraints

2. **Graceful Shutdown**: Always call `Stop()` with a timeout
   ```go
   ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
   defer cancel()
   sched.Stop(ctx)
   ```

3. **Error Handling**: Use `OnError` handler for observability
   ```go
   OnError: func(ctx context.Context, err error) {
       log.Printf("Scheduler error: %v", err)
       // Send to error tracking service
   }
   ```

4. **Horizontal Scaling**: Run multiple scheduler instances
   - Atomic locking prevents duplicate processing
   - Jobs are distributed across workers

5. **Monitoring**: Track scheduler state
   ```go
   if sched.IsIdle() {
       // No jobs in queue
   }
   if sched.IsProcessing() {
       // Currently processing a job
   }
   ```

## Examples

See the `examples/` directory:
- `examples/basic/`: In-memory implementation for testing
- `examples/mongodb/`: Production-ready MongoDB example

## Testing

Run tests:
```bash
go test ./...
```

Run with coverage:
```bash
go test -cover ./...
```

## Differences from mongodb-cron

This Go implementation maintains feature parity with the Node.js mongodb-cron package while adding:

- **Database abstraction**: Not limited to MongoDB
- **Go idioms**: Context support, proper error handling, concurrent-safe
- **Type safety**: Strongly typed API
- **Modern Go**: Uses Go modules, latest best practices

## License

MIT License - see LICENSE file for details

## Contributing

Contributions welcome! Please open an issue or PR.

Areas for contribution:
- Additional database implementations (PostgreSQL, Redis, etc.)
- Performance improvements
- More comprehensive tests
- Documentation improvements

## Credits

Inspired by [mongodb-cron](https://github.com/xpepermint/mongodb-cron) by Kristijan Sedlak.
