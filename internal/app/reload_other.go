//go:build !unix

package app

import (
	"log/slog"

	"go.uber.org/fx"
)

// RegisterReloadHook is a no-op on non-unix platforms.
func RegisterReloadHook(_ fx.Lifecycle, _ *slog.Logger) {}
