package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DEEJ4Y/mongodb-cron"
	"github.com/DEEJ4Y/mongodb-cron/mongodb"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	ctx := context.Background()

	// Connect to MongoDB
	mongoURI := os.Getenv("MONGO_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017"
	}

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		log.Fatalf("Failed to connect to MongoDB: %v", err)
	}
	defer client.Disconnect(ctx)

	// Get collection
	collection := client.Database("test").Collection("jobs")

	// Create MongoDB store
	store, err := mongodb.NewStore(mongodb.Config{
		Collection: collection,
		// Optional: add custom field paths
		// SleepUntilField: "customSleepUntil",
		// Optional: add filter condition
		// Condition: bson.M{"type": "email"},
	})
	if err != nil {
		log.Fatalf("Failed to create store: %v", err)
	}

	// Create scheduler
	sched, err := scheduler.New(scheduler.Config{
		Store: store,
		OnDocument: func(ctx context.Context, job *scheduler.Job) error {
			fmt.Printf("Processing job: %v\n", job.Data)
			return nil
		},
		OnStart: func(ctx context.Context) error {
			fmt.Println("Scheduler started")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			fmt.Println("Scheduler stopped")
			return nil
		},
		OnIdle: func(ctx context.Context) error {
			fmt.Println("No jobs available, entering idle mode")
			return nil
		},
		OnError: func(ctx context.Context, err error) {
			fmt.Printf("Error: %v\n", err)
		},
		NextDelay:      1 * time.Second,
		ReprocessDelay: 1 * time.Second,
		IdleDelay:      10 * time.Second,
		LockDuration:   10 * time.Minute,
	})
	if err != nil {
		log.Fatalf("Failed to create scheduler: %v", err)
	}

	// Insert sample jobs
	now := time.Now()
	jobs := []interface{}{
		bson.M{
			"name":       "Job #1 - Immediate",
			"sleepUntil": now,
		},
		bson.M{
			"name":       "Job #2 - Deferred 2s",
			"sleepUntil": now.Add(2 * time.Second),
		},
		bson.M{
			"name":       "Job #3 - Deferred 3s",
			"sleepUntil": now.Add(3 * time.Second),
		},
		bson.M{
			"name":       "Job #4 - Recurring every 5 seconds",
			"sleepUntil": now,
			"interval":   "*/5 * * * * *", // every 5 seconds
		},
		bson.M{
			"name":       "Job #5 - Auto-remove",
			"sleepUntil": now.Add(5 * time.Second),
			"autoRemove": true,
		},
		bson.M{
			"name":        "Job #6 - Recurring with expiration",
			"sleepUntil":  now,
			"interval":    "*/2 * * * * *", // every 2 seconds
			"repeatUntil": now.Add(10 * time.Second),
		},
	}

	result, err := collection.InsertMany(ctx, jobs)
	if err != nil {
		log.Fatalf("Failed to insert jobs: %v", err)
	}
	fmt.Printf("Inserted %d jobs\n", len(result.InsertedIDs))

	// Create index for better performance
	indexModel := mongo.IndexModel{
		Keys: bson.D{{Key: "sleepUntil", Value: 1}},
		Options: options.Index().SetSparse(true),
	}
	_, err = collection.Indexes().CreateOne(ctx, indexModel)
	if err != nil {
		log.Printf("Warning: Failed to create index: %v", err)
	}

	// Start scheduler
	if err := sched.Start(ctx); err != nil {
		log.Fatalf("Failed to start scheduler: %v", err)
	}

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	// Graceful shutdown
	fmt.Println("\nShutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := sched.Stop(shutdownCtx); err != nil {
		log.Printf("Error during shutdown: %v", err)
	}
}
