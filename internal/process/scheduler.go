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
	scheduler := cron.New(
		cron.WithLocation(settings.Location),
	)
	for _, runner := range runners {
		job := func() {
			runner.runScheduled(ctx)
		}
		if _, scheduleErr := scheduler.AddFunc(settings.CronSpec, job); scheduleErr != nil {
			return nil, fmt.Errorf("schedule digest: %w", scheduleErr)
		}
	}

	return scheduler, nil
}
