// Package logx gives each package a logger that tags its lines with the
// package name (D-82). It resolves slog's default logger at each call, so
// loggers declared in package variables use the handler main installs.
package logx

import "log/slog"

// Pkg is a package's logger.
type Pkg string

func (p Pkg) args(a []any) []any { return append(a, "pkg", string(p)) }

// Debug logs at debug level.
func (p Pkg) Debug(msg string, a ...any) { slog.Default().Debug(msg, p.args(a)...) }

// Info logs at info level.
func (p Pkg) Info(msg string, a ...any) { slog.Default().Info(msg, p.args(a)...) }

// Warn logs at warning level.
func (p Pkg) Warn(msg string, a ...any) { slog.Default().Warn(msg, p.args(a)...) }

// Error logs at error level.
func (p Pkg) Error(msg string, a ...any) { slog.Default().Error(msg, p.args(a)...) }
