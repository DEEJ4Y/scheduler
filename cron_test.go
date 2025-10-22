package scheduler

import (
	"testing"
	"time"
)

func TestCalculateNextStart(t *testing.T) {
	t.Run("non-recurring job returns nil", func(t *testing.T) {
		job := &Job{
			Interval: "",
		}
		next, err := calculateNextStart(job, 0)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if next != nil {
			t.Error("expected nil for non-recurring job")
		}
	})

	t.Run("invalid cron expression returns error", func(t *testing.T) {
		job := &Job{
			Interval: "invalid cron",
		}
		next, err := calculateNextStart(job, 0)
		if err == nil {
			t.Error("expected error for invalid cron expression")
		}
		if next != nil {
			t.Error("expected nil when error occurs")
		}
	})

	t.Run("calculates next execution time", func(t *testing.T) {
		now := time.Now()
		job := &Job{
			SleepUntil: &now,
			Interval:   "*/5 * * * * *", // every 5 seconds (at :00, :05, :10, :15, etc.)
		}

		next, err := calculateNextStart(job, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if next == nil {
			t.Fatal("expected next execution time")
		}

		// Next execution should be at the next multiple of 5 seconds
		// Delay should be > 0 and <= 5 seconds
		diff := next.Sub(now)
		if diff <= 0 || diff > 5*time.Second {
			t.Errorf("expected 0 < delay <= 5 seconds, got %v", diff)
		}

		// The seconds value should be a multiple of 5
		if next.Second()%5 != 0 {
			t.Errorf("expected seconds to be multiple of 5, got %d", next.Second())
		}
	})

	t.Run("respects reprocess delay", func(t *testing.T) {
		now := time.Now()
		job := &Job{
			SleepUntil: &now,
			Interval:   "* * * * * *", // every second
		}

		reprocessDelay := 3 * time.Second
		next, err := calculateNextStart(job, reprocessDelay)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if next == nil {
			t.Fatal("expected next execution time")
		}

		// Should be at least reprocessDelay from now
		diff := next.Sub(now)
		if diff < reprocessDelay {
			t.Errorf("expected at least %v delay, got %v", reprocessDelay, diff)
		}
	})

	t.Run("returns nil when past repeatUntil", func(t *testing.T) {
		now := time.Now()
		past := now.Add(-1 * time.Hour)
		job := &Job{
			SleepUntil:  &now,
			Interval:    "* * * * * *",
			RepeatUntil: &past,
		}

		next, err := calculateNextStart(job, 0)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if next != nil {
			t.Error("expected nil when past repeatUntil")
		}
	})

	t.Run("returns now for old recurring jobs", func(t *testing.T) {
		// Job that should have run 1 hour ago
		past := time.Now().Add(-1 * time.Hour)
		job := &Job{
			SleepUntil: &past,
			Interval:   "* * * * * *", // every second
		}

		next, err := calculateNextStart(job, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if next == nil {
			t.Fatal("expected next execution time")
		}

		// Should return approximately now, not a past time
		now := time.Now()
		diff := next.Sub(now)
		if diff > 1*time.Second || diff < -1*time.Second {
			t.Errorf("expected approximately now, got diff of %v", diff)
		}
	})

	t.Run("handles various cron expressions", func(t *testing.T) {
		tests := []struct {
			name     string
			interval string
			wantErr  bool
		}{
			{"every second", "* * * * * *", false},
			{"every minute", "0 * * * * *", false},
			{"every hour", "0 0 * * * *", false},
			{"every 5 seconds", "*/5 * * * * *", false},
			{"every 15 minutes", "0 */15 * * * *", false},
			{"specific time", "0 0 12 * * *", false},
			{"invalid", "not a cron", true},
			{"too few fields", "* *", true},
		}

		now := time.Now()
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				job := &Job{
					SleepUntil: &now,
					Interval:   tt.interval,
				}

				next, err := calculateNextStart(job, 0)

				if tt.wantErr {
					if err == nil {
						t.Error("expected error but got none")
					}
				} else {
					if err != nil {
						t.Errorf("unexpected error: %v", err)
					}
					if next == nil {
						t.Error("expected next time but got nil")
					}
				}
			})
		}
	})
}
