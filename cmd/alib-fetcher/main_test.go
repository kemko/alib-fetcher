package main

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/require"
)

func Test_runScheduler_executes_job_immediately_before_waiting_for_schedule(t *testing.T) {
	// Given
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scheduler := cron.New()
	var runs atomic.Int32
	job := func() {
		runs.Add(1)
	}
	_, err := scheduler.AddFunc("0 0 1 1 *", job)
	require.NoError(t, err)

	// When
	runScheduler(ctx, scheduler, job)

	// Then
	require.Equal(t, int32(1), runs.Load())
}
