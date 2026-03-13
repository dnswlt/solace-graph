package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// cliHandler is a minimal slog.Handler for CLI output.
// Format: "15:04:05 LEVEL message key=value ..."
type cliHandler struct {
	w     *os.File
	level slog.Level
}

func (h *cliHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *cliHandler) Handle(_ context.Context, r slog.Record) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s %s", r.Time.Format(time.TimeOnly), r.Level, r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&sb, " %s=%v", a.Key, a.Value.Any())
		return true
	})
	sb.WriteByte('\n')
	_, err := fmt.Fprint(h.w, sb.String())
	return err
}

func (h *cliHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *cliHandler) WithGroup(_ string) slog.Handler      { return h }
