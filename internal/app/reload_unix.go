//go:build unix

package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/fx"

	"doppelgaenger/internal/config"
	"doppelgaenger/internal/logging"
)

var execProcess = syscall.Exec

// RegisterReloadHook handles SIGHUP as a config reload trigger.
// On success it re-execs the current process image, which recreates all
// components with fresh configuration while keeping the same PID.
func RegisterReloadHook(lc fx.Lifecycle, logger *slog.Logger) {
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})

	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			signal.Notify(ch, syscall.SIGHUP)

			go func() {
				for {
					select {
					case <-done:
						return
					case <-ch:
						handleSIGHUP(logger)
					}
				}
			}()

			return nil
		},
		OnStop: func(_ context.Context) error {
			close(done)
			signal.Stop(ch)

			return nil
		},
	})
}

func handleSIGHUP(logger *slog.Logger) {
	logger.Log(context.Background(), logging.LevelNotice, "reload requested", "signal", "SIGHUP")

	// Validate config before replacing the process image.
	if _, err := config.Load(); err != nil {
		logger.Error("reload aborted: invalid config", "err", err)
		return
	}

	if err := reexecSelf(); err != nil {
		logger.Error("reload failed", "err", err)
		return
	}
}

func reexecSelf() error {
	exe, err := os.Executable()
	if err == nil {
		if execErr := execProcess(exe, os.Args, os.Environ()); execErr == nil {
			return nil
		}
	}

	// Fallback that often works even if argv[0]/binary path is unavailable.
	if err = execProcess("/proc/self/exe", os.Args, os.Environ()); err != nil {
		return errors.New("exec failed for both executable path and /proc/self/exe: " + err.Error())
	}

	return nil
}
