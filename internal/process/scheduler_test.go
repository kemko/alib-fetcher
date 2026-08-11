package process

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/require"
)

func Test_runScheduler_executes_startup_job_before_starting_scheduler(t *testing.T) {
	// Given
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	scheduler := cron.New()
	events := make(chan string, 2)
	scheduler.Schedule(&runImmediatelyOnce{}, cron.FuncJob(func() {
		events <- "scheduled"
		cancel()
	}))
	startupJob := func() {
		events <- "startup"
	}

	// When
	runScheduler(ctx, scheduler, startupJob, true)

	// Then
	require.Equal(t, []string{"startup", "scheduled"}, drainEvents(events))
}

func Test_runScheduler_skips_startup_job_but_starts_scheduler_when_disabled(t *testing.T) {
	// Given
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	scheduler := cron.New()
	events := make(chan string, 2)
	scheduler.Schedule(&runImmediatelyOnce{}, cron.FuncJob(func() {
		events <- "scheduled"
		cancel()
	}))
	startupJob := func() {
		events <- "startup"
	}

	// When
	runScheduler(ctx, scheduler, startupJob, false)

	// Then
	require.Equal(t, []string{"scheduled"}, drainEvents(events))
}

func Test_runScheduler_waits_for_running_scheduler_jobs_on_shutdown(t *testing.T) {
	// Given
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler := cron.New()
	jobStarted := make(chan struct{})
	releaseJob := make(chan struct{})
	scheduler.Schedule(&runImmediatelyOnce{}, cron.FuncJob(func() {
		close(jobStarted)
		cancel()
		<-releaseJob
	}))
	done := make(chan struct{})

	// When
	go func() {
		runScheduler(ctx, scheduler, func() {}, false)
		close(done)
	}()
	waitForSignal(t, jobStarted)

	// Then
	select {
	case <-done:
		close(releaseJob)
		t.Fatal("scheduler shutdown returned before running job completed")
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseJob)
	waitForSignal(t, done)
}

type runImmediatelyOnce struct {
	used atomic.Bool
}

func (s *runImmediatelyOnce) Next(now time.Time) time.Time {
	if s.used.Swap(true) {
		return time.Time{}
	}

	return now
}

func drainEvents(events <-chan string) []string {
	var items []string
	for {
		select {
		case event := <-events:
			items = append(items, event)
		default:
			return items
		}
	}
}
