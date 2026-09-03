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
	"strconv"
	"syscall"
	"time"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/app"
	"github.com/kemko/alib-fetcher/internal/config"
	"github.com/kemko/alib-fetcher/internal/process"
	"github.com/kemko/alib-fetcher/internal/slink"
	"github.com/kemko/alib-fetcher/internal/telegram"
)

const logKeyError = "error"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("service.failed", slog.Any(logKeyError, err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	once := flag.Bool("once", false, "fetch and send one digest, then exit")
	var forgetLatest forgetLatestOption
	flag.Var(&forgetLatest, "forget-latest", "delete the latest state records, then exit")
	flag.Parse()
	if forgetLatest.set {
		if forgetLatest.value <= 0 {
			return errors.New("-forget-latest must be positive")
		}
		if *once {
			return errors.New("-forget-latest is incompatible with -once")
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		return process.ForgetLatest(ctx, config.LoadStatePath(), forgetLatest.value, logger)
	}

	settings, err := config.Load()
	if err != nil {
		return err
	}
	fetcher, err := alib.NewClient(
		settings.AlibURL,
		settings.HTTPTimeout,
		settings.AlibRequestInterval,
		logger,
	)
	if err != nil {
		return err
	}
	telegramAdapter, err := telegram.NewSender(telegram.Config{
		APIBase: settings.TelegramAPIBase,
		Token:   settings.TelegramToken,
		ChatID:  settings.TelegramChatID,
		Timeout: settings.HTTPTimeout,
	})
	if err != nil {
		return err
	}
	var freshBooks app.FreshBooksPolicy
	if settings.FreshBooks != nil {
		freshBooks = settings.FreshBooks
	}
	var photoProcessor app.PhotoProcessor
	if settings.SlinkURL != "" {
		photoProcessor, err = slink.NewClient(
			settings.SlinkURL,
			settings.SlinkAPIKey,
			settings.SlinkTagID,
			settings.HTTPTimeout,
			logger,
		)
		if err != nil {
			return err
		}
	}
	dependencies := app.Dependencies{
		Fetcher:        fetcher,
		Sender:         telegramAdapter,
		FreshBooks:     freshBooks,
		Location:       settings.Location,
		MessageLimit:   settings.MessageLimit,
		PhotoProcessor: photoProcessor,
		Now:            time.Now,
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return process.Run(ctx, process.Settings{
		CronSpec:       settings.CronSpec(),
		Location:       settings.Location,
		RunOnStartup:   settings.RunOnStartup,
		StatePath:      settings.StatePath,
		TelegramChatID: settings.TelegramChatID,
	}, dependencies, telegramAdapter, *once, logger)
}

type forgetLatestOption struct {
	value int
	set   bool
}

func (option *forgetLatestOption) String() string {
	return strconv.Itoa(option.value)
}

func (option *forgetLatestOption) Set(value string) error {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("-forget-latest must be an integer: %w", err)
	}
	option.value = parsed
	option.set = true

	return nil
}
