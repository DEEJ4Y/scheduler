package mongodb

import (
	"context"
	"fmt"
	"time"

	"github.com/DEEJ4Y/scheduler"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Config holds the configuration for the MongoDB job store.
type Config struct {
	// Collection is the MongoDB collection where jobs are stored.
	// Required.
	Collection *mongo.Collection

	// Field names for job properties (optional, have defaults)
	SleepUntilField  string // default: "sleepUntil"
	IntervalField    string // default: "interval"
	RepeatUntilField string // default: "repeatUntil"
	AutoRemoveField  string // default: "autoRemove"

	// Condition is an optional additional filter to apply when querying jobs.
	// This allows you to process only a subset of jobs in the collection.
	// Example: bson.M{"type": "email"} to only process email jobs.
	Condition bson.M
}

// Store implements scheduler.JobStore for MongoDB.
type Store struct {
	collection       *mongo.Collection
	sleepUntilField  string
	intervalField    string
	repeatUntilField string
	autoRemoveField  string
	condition        bson.M
}

// NewStore creates a new MongoDB job store with the given configuration.
func NewStore(config Config) (*Store, error) {
	if config.Collection == nil {
		return nil, fmt.Errorf("collection is required")
	}

	// Set defaults
	if config.SleepUntilField == "" {
		config.SleepUntilField = "sleepUntil"
	}
	if config.IntervalField == "" {
		config.IntervalField = "interval"
	}
	if config.RepeatUntilField == "" {
		config.RepeatUntilField = "repeatUntil"
	}
	if config.AutoRemoveField == "" {
		config.AutoRemoveField = "autoRemove"
	}

	return &Store{
		collection:       config.Collection,
		sleepUntilField:  config.SleepUntilField,
		intervalField:    config.IntervalField,
		repeatUntilField: config.RepeatUntilField,
		autoRemoveField:  config.AutoRemoveField,
		condition:        config.Condition,
	}, nil
}

// LockNext atomically finds and locks the next available job.
func (s *Store) LockNext(ctx context.Context, lockUntil time.Time) (*scheduler.Job, error) {
	currentDate := time.Now()

	// Build the query filter
	filter := bson.M{
		"$and": []bson.M{
			{s.sleepUntilField: bson.M{"$exists": true, "$ne": nil}},
			{s.sleepUntilField: bson.M{"$not": bson.M{"$gt": currentDate}}},
		},
	}

	// Add custom condition if provided
	if s.condition != nil {
		andConditions := filter["$and"].([]bson.M)
		andConditions = append(andConditions, s.condition)
		filter["$and"] = andConditions
	}

	// Update to lock the job
	update := bson.M{
		"$set": bson.M{
			s.sleepUntilField: lockUntil,
		},
	}

	// Options: return document before update
	opts := options.FindOneAndUpdate().
		SetReturnDocument(options.Before)

	// Execute atomic findOneAndUpdate
	var result bson.M
	err := s.collection.FindOneAndUpdate(ctx, filter, update, opts).Decode(&result)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			// No job available
			return nil, nil
		}
		return nil, fmt.Errorf("findOneAndUpdate failed: %w", err)
	}

	// Convert BSON document to Job
	job, err := s.bsonToJob(result)
	if err != nil {
		return nil, fmt.Errorf("failed to convert document to job: %w", err)
	}

	return job, nil
}

// Update modifies a job's fields.
func (s *Store) Update(ctx context.Context, jobID interface{}, updates scheduler.JobUpdate) error {
	// Build update document
	updateDoc := bson.M{}

	if updates.SleepUntil != nil {
		// SleepUntil is a pointer to pointer
		// - if the inner pointer is nil, set field to null
		// - otherwise, set field to the time value
		if *updates.SleepUntil == nil {
			updateDoc[s.sleepUntilField] = nil
		} else {
			updateDoc[s.sleepUntilField] = **updates.SleepUntil
		}
	}

	if len(updateDoc) == 0 {
		// Nothing to update
		return nil
	}

	// Perform update
	filter := bson.M{"_id": jobID}
	update := bson.M{"$set": updateDoc}

	result, err := s.collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("update failed: %w", err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("job not found: %v", jobID)
	}

	return nil
}

// Remove deletes a job from the store.
func (s *Store) Remove(ctx context.Context, jobID interface{}) error {
	filter := bson.M{"_id": jobID}

	result, err := s.collection.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("delete failed: %w", err)
	}

	if result.DeletedCount == 0 {
		return fmt.Errorf("job not found: %v", jobID)
	}

	return nil
}

// bsonToJob converts a BSON document to a Job struct.
func (s *Store) bsonToJob(doc bson.M) (*scheduler.Job, error) {
	job := &scheduler.Job{
		Data: make(map[string]interface{}),
	}

	// Extract _id
	if id, ok := doc["_id"]; ok {
		job.ID = id
	}

	// Extract sleepUntil
	if sleepUntil, ok := doc[s.sleepUntilField]; ok && sleepUntil != nil {
		if t, ok := sleepUntil.(primitive.DateTime); ok {
			timeVal := t.Time()
			job.SleepUntil = &timeVal
		}
	}

	// Extract interval
	if interval, ok := doc[s.intervalField]; ok && interval != nil {
		if str, ok := interval.(string); ok {
			job.Interval = str
		}
	}

	// Extract repeatUntil
	if repeatUntil, ok := doc[s.repeatUntilField]; ok && repeatUntil != nil {
		if t, ok := repeatUntil.(primitive.DateTime); ok {
			timeVal := t.Time()
			job.RepeatUntil = &timeVal
		}
	}

	// Extract autoRemove
	if autoRemove, ok := doc[s.autoRemoveField]; ok && autoRemove != nil {
		if b, ok := autoRemove.(bool); ok {
			job.AutoRemove = b
		}
	}

	// Copy all fields to Data
	for key, value := range doc {
		job.Data[key] = value
	}

	return job, nil
}
