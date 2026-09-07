package process

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func Test_serviceGeneration_waits_for_scheduled_digest_before_stopping(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	snapshot := reloadTestSnapshot(t, "scheduled", &blockingFetcher{
		started: started,
		release: release,
	}, false)
	snapshot.Settings.CronSpec = "@every 1s"
	generation, err := startServiceGeneration(ctx, snapshot, nil, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	waitForSignal(t, started)
	done := make(chan struct{})

	go func() {
		generation.stop()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("generation stopped before its scheduled digest finished")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	waitForSignal(t, done)
}
