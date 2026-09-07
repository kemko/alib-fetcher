package process

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"time"

	"github.com/robfig/cron/v3"
)

const reloadInterval = time.Second

// ReloadSnapshot contains one complete service generation.
type ReloadSnapshot struct { //nolint:govet // immutable configuration stays grouped with its apply hook.
	Key        any
	Settings   Settings
	Recipients []Recipient
	Prepare    func() error
}

// ReloadFunc loads and validates the current service configuration.
type ReloadFunc func(context.Context) (ReloadSnapshot, error)

type serviceGeneration struct { //nolint:govet // lifecycle handles stay grouped by shutdown ownership.
	runners       []*digestRunner
	controlCtx    context.Context
	previous      *ReloadSnapshot
	scheduler     *cron.Cron
	cancel        context.CancelFunc
	callbacksDone <-chan struct{}
	done          chan struct{}
}

// RunReloadable runs the service and applies valid configuration changes.
func RunReloadable(
	ctx context.Context,
	initial ReloadSnapshot,
	reload ReloadFunc,
	logger *slog.Logger,
) error {
	if len(initial.Recipients) == 0 {
		return errors.New("run process: no recipients configured")
	}
	if reload == nil {
		return runServiceGeneration(ctx, initial, logger)
	}
	if err := prepareReloadSnapshot(initial); err != nil {
		return err
	}

	generation, err := startServiceGeneration(ctx, initial, initialStartupChats(initial), logger)
	if err != nil {
		return err
	}
	lastReloadError := ""
	ticker := time.NewTicker(reloadInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			generation.stop()

			return nil
		case <-ticker.C:
			generation, lastReloadError, err = reloadGeneration(
				ctx,
				generation,
				reload,
				lastReloadError,
				logger,
			)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}

				return err
			}
		}
	}
}

func reloadGeneration(
	ctx context.Context,
	generation *serviceGeneration,
	reload ReloadFunc,
	lastReloadError string,
	logger *slog.Logger,
) (*serviceGeneration, string, error) {
	candidate, loadErr := reload(ctx)
	if loadErr != nil {
		if ctx.Err() != nil {
			generation.stop()

			return generation, lastReloadError, ctx.Err()
		}
		message := loadErr.Error()
		if message != lastReloadError {
			logger.ErrorContext(ctx, "config.reload_failed", slog.String(logKeyError, "candidate rejected"))
			lastReloadError = message
		}

		return generation, lastReloadError, nil
	}
	if sameReloadSnapshot(generation.snapshot(), candidate) {
		return generation, "", nil
	}

	logger.InfoContext(ctx, "config.reload_pending")
	previous := *generation.previous
	generation.stop()
	latest, latestErr := reload(ctx)
	if latestErr != nil {
		if ctx.Err() != nil {
			return generation, "", ctx.Err()
		}
		logger.ErrorContext(ctx, "config.reload_failed", slog.String(logKeyError, "candidate rejected"))
		restarted, startErr := startServiceGeneration(ctx, previous, nil, logger)

		return restarted, latestErr.Error(), startErr
	}
	if sameReloadSnapshot(previous, latest) {
		restarted, startErr := startServiceGeneration(ctx, previous, nil, logger)

		return restarted, "", startErr
	}
	if prepareErr := prepareReloadSnapshot(latest); prepareErr != nil {
		logger.ErrorContext(ctx, "config.reload_failed", slog.String(logKeyError, "candidate rejected"))
		restarted, startErr := startServiceGeneration(ctx, previous, nil, logger)

		return restarted, prepareErr.Error(), startErr
	}
	restarted, startErr := startServiceGeneration(
		ctx,
		latest,
		newRecipientStartupChats(previous, latest),
		logger,
	)
	if startErr == nil {
		logger.InfoContext(ctx, "config.reloaded")
	}

	return restarted, "", startErr
}

func startServiceGeneration(
	parent context.Context,
	snapshot ReloadSnapshot,
	startupChats map[string]struct{},
	logger *slog.Logger,
) (*serviceGeneration, error) {
	controlCtx, cancel := context.WithCancel(parent)
	runners := make([]*digestRunner, 0, len(snapshot.Recipients))
	for _, recipient := range snapshot.Recipients {
		runners = append(runners, newDigestRunnerForRecipient(recipient, logger))
	}
	scheduler, err := newSchedulerForRunnersWithRunContext(
		controlCtx,
		parent,
		snapshot.Settings,
		runners,
	)
	if err != nil {
		cancel()

		return nil, err
	}
	generation := &serviceGeneration{
		cancel:     cancel,
		controlCtx: controlCtx,
		scheduler:  scheduler,
		runners:    runners,
		done:       make(chan struct{}),
		previous:   &snapshot,
	}
	generation.callbacksDone = startCallbackGroupsWithRunContext(
		controlCtx,
		parent,
		snapshot.Recipients,
		runners,
		logger,
	)
	logger.InfoContext(parent, "scheduler.started",
		slog.String(logKeySchedule, snapshot.Settings.CronSpec),
		slog.String(logKeyTimezone, snapshot.Settings.Location.String()),
	)
	go generation.run(startupChats, parent, logger)

	return generation, nil
}

func (generation *serviceGeneration) run(
	startupChats map[string]struct{},
	runCtx context.Context,
	logger *slog.Logger,
) {
	defer close(generation.done)
	for index, runner := range generation.runners {
		if _, startup := startupChats[generation.previous.Recipients[index].ChatID]; startup {
			go runner.runStartup(runCtx)
		}
	}
	generation.scheduler.Start()
	<-generation.controlCtx.Done()
	<-generation.scheduler.Stop().Done()
	for _, runner := range generation.runners {
		runner.stopAndWait()
	}
	<-generation.callbacksDone
	logger.InfoContext(runCtx, "scheduler.stopped")
}

func (generation *serviceGeneration) stop() {
	for _, runner := range generation.runners {
		runner.requestStop()
	}
	generation.cancel()
	<-generation.done
}

func (generation *serviceGeneration) snapshot() ReloadSnapshot {
	return *generation.previous
}

func sameReloadSnapshot(left, right ReloadSnapshot) bool {
	if left.Key != nil || right.Key != nil {
		return reflect.DeepEqual(left.Key, right.Key)
	}

	return reflect.DeepEqual(left.Settings, right.Settings)
}

func runServiceGeneration(ctx context.Context, snapshot ReloadSnapshot, logger *slog.Logger) error {
	if err := prepareReloadSnapshot(snapshot); err != nil {
		return err
	}
	generation, err := startServiceGeneration(ctx, snapshot, initialStartupChats(snapshot), logger)
	if err != nil {
		return err
	}
	<-ctx.Done()
	generation.stop()

	return nil
}

func prepareReloadSnapshot(snapshot ReloadSnapshot) error {
	if snapshot.Prepare == nil {
		return nil
	}

	return snapshot.Prepare()
}

func initialStartupChats(snapshot ReloadSnapshot) map[string]struct{} {
	if !snapshot.Settings.RunOnStartup {
		return nil
	}
	return recipientIDs(snapshot.Recipients)
}

func newRecipientStartupChats(previous, next ReloadSnapshot) map[string]struct{} {
	if !next.Settings.RunOnStartup {
		return nil
	}
	previousIDs := recipientIDs(previous.Recipients)
	startup := make(map[string]struct{})
	for _, recipient := range next.Recipients {
		if _, exists := previousIDs[recipient.ChatID]; !exists {
			startup[recipient.ChatID] = struct{}{}
		}
	}

	return startup
}

func recipientIDs(recipients []Recipient) map[string]struct{} {
	ids := make(map[string]struct{}, len(recipients))
	for _, recipient := range recipients {
		ids[recipient.ChatID] = struct{}{}
	}

	return ids
}

func (r *digestRunner) requestStop() {
	r.lifecycle.Lock()
	r.stopping = true
	r.lifecycle.Unlock()
}
