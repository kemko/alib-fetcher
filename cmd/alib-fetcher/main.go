// Package main wires the alib-fetcher service process.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/app"
	"github.com/kemko/alib-fetcher/internal/config"
	"github.com/kemko/alib-fetcher/internal/process"
	"github.com/kemko/alib-fetcher/internal/telegram"
)

const (
	logKeyError  = "error"
	logKeyChatID = "chat_id"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("service.failed", slog.Any(logKeyError, err))
		var commandErr commandError
		if errors.As(err, &commandErr) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	options, err := parseCommandLine()
	if err != nil {
		return err
	}
	if options.help {
		return nil
	}
	if options.forgetLatest.set {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		statePath, loadErr := config.LoadForMaintenance(options.configPath, options.chatID)
		if loadErr != nil {
			return loadErr
		}
		return process.ForgetLatestForChat(ctx, statePath, options.forgetLatest.value, options.chatID, logger)
	}
	settings, err := config.Load(options.configPath)
	if err != nil {
		return err
	}
	return runWithConfigForChat(logger, settings, options.once, options.chatID)
}

type commandOptions struct {
	configPath   string
	chatID       string
	help         bool
	service      bool
	once         bool
	forgetLatest forgetLatestOption
}

func parseCommandLine() (commandOptions, error) {
	return parseCommandLineArgs(os.Args, os.Stdout, os.Stderr)
}

func parseCommandLineArgs(args []string, writer, errWriter io.Writer) (commandOptions, error) {
	var options commandOptions
	command := &cli.Command{
		Name:            args[0],
		Usage:           "fetch and send Alib book listings",
		HideHelpCommand: true,
		Writer:          writer,
		ErrWriter:       errWriter,
		ExitErrHandler:  func(context.Context, *cli.Command, error) {},
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "service", Usage: "run the scheduled service"},
			&cli.BoolFlag{Name: "once", Usage: "fetch and send one digest, then exit"},
			&cli.StringFlag{Name: "config", Value: "./config.toml", Usage: "path to TOML configuration"},
			&cli.StringFlag{Name: "chat", Usage: "recipient chat ID"},
			&cli.IntFlag{
				Name:   "forget-latest",
				Usage:  "delete the latest state records, then exit",
				Config: cli.IntegerConfig{Base: 10},
			},
		},
	}
	command.Action = func(_ context.Context, command *cli.Command) error {
		if len(args) == 1 {
			options.help = true
			return cli.ShowRootCommandHelp(command)
		}
		if command.NArg() > 0 {
			return reportCommandError(command, invalidArguments("positional arguments are not supported"))
		}

		chatID := command.String("chat")
		if command.IsSet("chat") {
			var err error
			chatID, err = config.NormalizeChatID(chatID)
			if err != nil {
				return reportCommandError(command, commandError{err: err})
			}
		}
		options = commandOptions{
			configPath: command.String("config"),
			chatID:     chatID,
			service:    command.Bool("service"),
			once:       command.Bool("once"),
			forgetLatest: forgetLatestOption{
				value: command.Int("forget-latest"),
				set:   command.IsSet("forget-latest"),
			},
		}
		validated, err := validateCommandOptions(options)
		if err != nil {
			return reportCommandError(command, err)
		}
		options = validated

		return nil
	}

	if err := command.Run(context.Background(), args); err != nil {
		if command.IsSet("help") {
			return commandOptions{help: true}, nil
		}
		var commandErr commandError
		if errors.As(err, &commandErr) {
			return commandOptions{}, err
		}
		return commandOptions{}, commandError{err: err}
	}
	if command.IsSet("help") {
		return commandOptions{help: true}, nil
	}

	return options, nil
}

func validateCommandOptions(options commandOptions) (commandOptions, error) {
	modeCount := 0
	if options.service {
		modeCount++
	}
	if options.once {
		modeCount++
	}
	if options.forgetLatest.set {
		modeCount++
	}
	if modeCount != 1 {
		return options, invalidArguments("exactly one of -service, -once, or -forget-latest must be selected")
	}
	if options.service && options.chatID != "" {
		return options, invalidArguments("-chat is incompatible with -service")
	}
	if options.forgetLatest.set && options.forgetLatest.value <= 0 {
		return options, invalidArguments("-forget-latest must be positive")
	}
	if options.forgetLatest.set && options.chatID == "" {
		return options, invalidArguments("-chat is required with -forget-latest")
	}
	return options, nil
}

func runWithConfig(logger *slog.Logger, settings config.Config, once bool) error {
	return runWithConfigForChat(logger, settings, once, "")
}

func runWithConfigForChat(logger *slog.Logger, settings config.Config, once bool, selectedChat string) error {
	factory := newRuntimeFactory(logger)
	initial, err := factory.snapshot(settings, selectedChat)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if once {
		return process.RunRecipients(ctx, initial.Settings, initial.Recipients, true, logger)
	}

	return process.RunReloadable(ctx, initial, func(loadCtx context.Context) (process.ReloadSnapshot, error) {
		if loadErr := loadCtx.Err(); loadErr != nil {
			return process.ReloadSnapshot{}, loadErr
		}
		latest, loadErr := config.Load(settings.Path)
		if loadErr != nil {
			return process.ReloadSnapshot{}, loadErr
		}

		return factory.snapshot(latest, selectedChat)
	}, logger)
}

type runtimeFactory struct {
	clients map[string]*telegram.Client
	logger  *slog.Logger
}

func newRuntimeFactory(logger *slog.Logger) *runtimeFactory {
	return &runtimeFactory{
		clients: make(map[string]*telegram.Client),
		logger:  logger,
	}
}

func (factory *runtimeFactory) snapshot(
	settings config.Config,
	selectedChat string,
) (process.ReloadSnapshot, error) {
	recipients := make([]process.Recipient, 0, len(settings.Chats))
	usedClients := make(map[*telegram.Client]struct{}, len(settings.Chats))
	for _, chat := range settings.Chats {
		if selectedChat != "" && chat.ChatID != selectedChat {
			continue
		}
		fetcher, err := alib.NewClient(
			chat.AlibURLs,
			settings.HTTPTimeout,
			settings.AlibMaxRetries,
			factory.logger.With(slog.String(logKeyChatID, chat.ChatID)),
		)
		if err != nil {
			return process.ReloadSnapshot{}, err
		}
		telegramClient := factory.clients[chat.TelegramToken]
		if telegramClient == nil {
			telegramClient, err = telegram.NewClient(telegram.ClientConfig{
				Token:   chat.TelegramToken,
				Timeout: settings.HTTPTimeout,
			})
			if err != nil {
				return process.ReloadSnapshot{}, err
			}
			factory.clients[chat.TelegramToken] = telegramClient
		}
		usedClients[telegramClient] = struct{}{}
		telegramAdapter, err := telegramClient.NewSender(chat.ChatID)
		if err != nil {
			return process.ReloadSnapshot{}, err
		}
		var freshBooks app.FreshBooksPolicy
		if settings.FreshBooks != nil {
			freshBooks = settings.FreshBooks
		}
		recipients = append(recipients, process.Recipient{
			ChatID:    chat.ChatID,
			StatePath: chat.StatePath,
			Callbacks: telegramClient,
			Dependencies: app.Dependencies{
				Fetcher:      fetcher,
				Sender:       telegramAdapter,
				FreshBooks:   freshBooks,
				Location:     settings.Location,
				MessageLimit: settings.MessageLimit,
				Now:          time.Now,
			},
		})
	}
	if len(recipients) == 0 {
		if selectedChat == "" {
			return process.ReloadSnapshot{}, errors.New("no recipients configured")
		}
		return process.ReloadSnapshot{}, fmt.Errorf("unknown chat %q", selectedChat)
	}

	return process.ReloadSnapshot{
		Settings: process.Settings{
			CronSpec:     settings.CronSchedule,
			Location:     settings.Location,
			RunOnStartup: settings.RunOnStartup,
		},
		Recipients: recipients,
		Key:        settings,
		Prepare: func() error {
			for client := range usedClients {
				if err := client.SetTimeout(settings.HTTPTimeout); err != nil {
					return err
				}
			}

			return nil
		},
	}, nil
}

type forgetLatestOption struct {
	value int
	set   bool
}

type commandError struct{ err error }

func (err commandError) Error() string { return err.err.Error() }

func (err commandError) Unwrap() error { return err.err }

func invalidArguments(message string) error {
	return commandError{err: errors.New(message)}
}

func reportCommandError(command *cli.Command, err error) error {
	if _, writeErr := fmt.Fprintln(command.ErrWriter, "error:", err); writeErr != nil {
		return commandError{err: errors.Join(err, writeErr)}
	}
	if helpErr := cli.ShowRootCommandHelp(command); helpErr != nil {
		return commandError{err: errors.Join(err, helpErr)}
	}

	return err
}
