package process

import (
	"context"
	"fmt"

	"github.com/robfig/cron/v3"
)

func newScheduler(ctx context.Context, settings Settings, runner *digestRunner) (*cron.Cron, error) {
	scheduler := cron.New(
		cron.WithLocation(settings.Location),
	)
	job := func() {
		runner.runScheduled(ctx)
	}
	if _, scheduleErr := scheduler.AddFunc(settings.CronSpec, job); scheduleErr != nil {
		return nil, fmt.Errorf("schedule digest: %w", scheduleErr)
	}

	return scheduler, nil
}

func runScheduler(ctx context.Context, scheduler *cron.Cron, initialJob func(), runOnStartup bool) {
	if runOnStartup {
		initialJob()
	}
	scheduler.Start()
	<-ctx.Done()
	<-scheduler.Stop().Done()
}
