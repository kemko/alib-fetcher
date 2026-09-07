package process

import (
	"context"
	"fmt"

	"github.com/robfig/cron/v3"
)

func newSchedulerForRunners(
	ctx context.Context,
	settings Settings,
	runners []*digestRunner,
) (*cron.Cron, error) {
	return newSchedulerForRunnersWithRunContext(ctx, ctx, settings, runners)
}

func newSchedulerForRunnersWithRunContext(
	_ context.Context,
	runCtx context.Context,
	settings Settings,
	runners []*digestRunner,
) (*cron.Cron, error) {
	scheduler := cron.New(
		cron.WithLocation(settings.Location),
	)
	for _, runner := range runners {
		job := func() {
			runner.runScheduled(runCtx)
		}
		if _, scheduleErr := scheduler.AddFunc(settings.CronSpec, job); scheduleErr != nil {
			return nil, fmt.Errorf("schedule digest: %w", scheduleErr)
		}
	}

	return scheduler, nil
}

func runSchedulerForRunners(
	ctx context.Context,
	scheduler *cron.Cron,
	runners []*digestRunner,
	runOnStartup bool,
) {
	if runOnStartup {
		for _, runner := range runners {
			go runner.runStartup(ctx)
		}
	}
	scheduler.Start()
	<-ctx.Done()
	for _, runner := range runners {
		runner.requestStop()
	}
	<-scheduler.Stop().Done()
	for _, runner := range runners {
		runner.stopAndWait()
	}
}

func runScheduler(ctx context.Context, scheduler *cron.Cron, initialJob func(), runOnStartup bool) {
	if runOnStartup {
		initialJob()
	}
	scheduler.Start()
	<-ctx.Done()
	<-scheduler.Stop().Done()
}
