// Package main wires the alib-fetcher service process.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/app"
	"github.com/kemko/alib-fetcher/internal/config"
	"github.com/kemko/alib-fetcher/internal/store"
	"github.com/kemko/alib-fetcher/internal/telegram"

	"github.com/robfig/cron/v3"
)

const (
	logKeyError        = "error"
	logKeyFetched      = "fetched"
	logKeyNew          = "new"
	logKeyPruned       = "pruned"
	logKeySchedule     = "cron_schedule"
	logKeySent         = "sent"
	logKeyTimezone     = "timezone"
	logKeyTrigger      = "trigger"
	logKeyUpdateOffset = "update_offset"
	triggerRefresh     = "refresh"
	triggerScheduled   = "scheduled"
	triggerStartup     = "startup"
)

const (
	refreshAlreadyRunningText = "Проверка уже выполняется"
	refreshStartedText        = "Проверяю новые книги"
)

const (
	callbackPollErrorDelay = 5 * time.Second
	callbackPollIdleDelay  = time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("service.failed", slog.Any(logKeyError, err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	once := flag.Bool("once", false, "fetch and send one digest, then exit")
	flag.Parse()

	settings, err := config.Load()
	if err != nil {
		return err
	}
	fetcher, err := alib.NewClient(settings.AlibURL, settings.HTTPTimeout)
	if err != nil {
		return err
	}
	sender, err := telegram.NewSender(telegram.Config{
		APIBase: settings.TelegramAPIBase,
		Token:   settings.TelegramToken,
		ChatID:  settings.TelegramChatID,
		Timeout: settings.HTTPTimeout,
	})
	if err != nil {
		return err
	}
	dependencies := app.Dependencies{
		Fetcher:      fetcher,
		Sender:       sender,
		MessageLimit: settings.MessageLimit,
		Now:          time.Now,
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return runProcess(ctx, processSettings{
		CronSpec:     settings.CronSpec(),
		Location:     settings.Location,
		RunOnStartup: settings.RunOnStartup,
		StatePath:    settings.StatePath,
	}, dependencies, sender, *once, logger)
}

type processSettings struct {
	Location     *time.Location
	CronSpec     string
	StatePath    string
	RunOnStartup bool
}

type callbackClient interface {
	PollCallbacks(ctx context.Context, offset int) ([]telegram.Callback, int, error)
	AnswerCallback(ctx context.Context, callbackID string, text string) error
	RemoveReplyMarkup(ctx context.Context, chatID int64, messageID int) error
}

func runProcess(
	ctx context.Context,
	settings processSettings,
	dependencies app.Dependencies,
	callbacks callbackClient,
	once bool,
	logger *slog.Logger,
) error {
	if once {
		return executeJob(ctx, dependencies, settings.StatePath, logger)
	}

	runner := &digestRunner{
		dependencies: dependencies,
		logger:       logger,
		statePath:    settings.StatePath,
	}
	scheduler := cron.New(
		cron.WithLocation(settings.Location),
	)
	job := func() {
		runner.RunScheduled(ctx)
	}
	if _, scheduleErr := scheduler.AddFunc(settings.CronSpec, job); scheduleErr != nil {
		return fmt.Errorf("schedule digest: %w", scheduleErr)
	}

	callbacksDone := startCallbackPolling(ctx, callbacks, runner, logger)
	logger.InfoContext(ctx, "scheduler.started",
		slog.String(logKeySchedule, settings.CronSpec),
		slog.String(logKeyTimezone, settings.Location.String()),
	)
	runScheduler(ctx, scheduler, func() { runner.RunStartup(ctx) }, settings.RunOnStartup)
	logger.InfoContext(ctx, "scheduler.stopped")
	<-callbacksDone
	runner.Wait()

	return nil
}

func runScheduler(ctx context.Context, scheduler *cron.Cron, initialJob func(), runOnStartup bool) {
	if runOnStartup {
		initialJob()
	}
	scheduler.Start()
	<-ctx.Done()
	<-scheduler.Stop().Done()
}

func executeJob(
	ctx context.Context,
	dependencies app.Dependencies,
	statePath string,
	logger *slog.Logger,
) (jobErr error) {
	logger.InfoContext(ctx, "digest.started")
	state, err := store.Open(statePath, dependencies.Now())
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := state.Close(); closeErr != nil {
			jobErr = errors.Join(jobErr, closeErr)
		}
	}()

	dependencies.State = state
	service := app.NewService(dependencies)
	result, err := service.Run(ctx)
	if err != nil {
		return err
	}
	logger.InfoContext(ctx, "digest.completed",
		slog.Int(logKeyFetched, result.Fetched),
		slog.Int(logKeyNew, result.New),
		slog.Int(logKeyPruned, result.Pruned),
		slog.Int(logKeySent, result.Sent),
	)

	return nil
}

type digestRunner struct {
	dependencies app.Dependencies
	logger       *slog.Logger
	statePath    string
	lock         sync.Mutex
	refreshRuns  sync.WaitGroup
}

func (r *digestRunner) RunStartup(ctx context.Context) {
	r.lock.Lock()
	defer r.lock.Unlock()

	r.runLocked(ctx, triggerStartup, nil, nil)
}

func (r *digestRunner) RunScheduled(ctx context.Context) {
	if !r.lock.TryLock() {
		return
	}
	defer r.lock.Unlock()

	r.runLocked(ctx, triggerScheduled, nil, nil)
}

func (r *digestRunner) TryStartRefresh(
	ctx context.Context,
	beforeDelivery func(context.Context) error,
	beforeRun func(context.Context) error,
) bool {
	if !r.lock.TryLock() {
		return false
	}

	r.refreshRuns.Add(1)
	go func() {
		defer r.refreshRuns.Done()
		defer r.lock.Unlock()

		r.runLocked(ctx, triggerRefresh, beforeRun, beforeDelivery)
	}()

	return true
}

func (r *digestRunner) Wait() {
	r.refreshRuns.Wait()
}

func (r *digestRunner) runLocked(
	ctx context.Context,
	trigger string,
	beforeRun func(context.Context) error,
	beforeDelivery func(context.Context) error,
) {
	if beforeRun != nil {
		if err := beforeRun(ctx); err != nil {
			r.logger.ErrorContext(ctx, "callback.answer_failed", slog.Any(logKeyError, err))
		}
	}

	dependencies := r.dependencies
	dependencies.BeforeDelivery = beforeDelivery
	if err := executeJob(ctx, dependencies, r.statePath, r.logger); err != nil {
		r.logger.ErrorContext(ctx, "digest.failed", slog.Any(logKeyError, err), slog.String(logKeyTrigger, trigger))
	}
}

func startCallbackPolling(
	ctx context.Context,
	callbacks callbackClient,
	runner *digestRunner,
	logger *slog.Logger,
) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		pollRefreshCallbacks(ctx, callbacks, runner, logger)
	}()

	return done
}

func pollRefreshCallbacks(
	ctx context.Context,
	callbacks callbackClient,
	runner *digestRunner,
	logger *slog.Logger,
) {
	offset := 0
	for ctx.Err() == nil {
		items, nextOffset, err := callbacks.PollCallbacks(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.ErrorContext(ctx, "callback.poll_failed",
				slog.Any(logKeyError, err),
				slog.Int(logKeyUpdateOffset, offset),
			)
			if !waitForCallbackPoll(ctx, callbackPollErrorDelay) {
				return
			}
			continue
		}
		offset = nextOffset
		if len(items) == 0 {
			if !waitForCallbackPoll(ctx, callbackPollIdleDelay) {
				return
			}
			continue
		}
		for _, callback := range items {
			if callback.Data != telegram.RefreshCallbackData {
				continue
			}
			handleRefreshCallback(ctx, callbacks, runner, callback, logger)
		}
	}
}

func waitForCallbackPoll(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func handleRefreshCallback(
	ctx context.Context,
	callbacks callbackClient,
	runner *digestRunner,
	callback telegram.Callback,
	logger *slog.Logger,
) {
	beforeDelivery := func(runCtx context.Context) error {
		if err := callbacks.RemoveReplyMarkup(runCtx, callback.MessageChatID, callback.MessageID); err != nil {
			return fmt.Errorf("remove refresh button: %w", err)
		}

		return nil
	}
	started := runner.TryStartRefresh(ctx, beforeDelivery, func(runCtx context.Context) error {
		return callbacks.AnswerCallback(runCtx, callback.ID, refreshStartedText)
	})
	if started {
		return
	}

	if err := callbacks.AnswerCallback(ctx, callback.ID, refreshAlreadyRunningText); err != nil {
		logger.ErrorContext(ctx, "callback.answer_failed", slog.Any(logKeyError, err))
	}
}
