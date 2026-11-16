// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package logger

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// NewZap builds the zap logger shared by the network functions: console encoding on stdout with
// ISO8601 timestamps, capitalized levels and short callers, and no stack traces. It returns the logger
// together with the level that controls it, which starts at info.
func NewZap() (*zap.Logger, zap.AtomicLevel) {
	atomicLevel := zap.NewAtomicLevelAt(zap.InfoLevel)
	config := zap.Config{
		Level:            atomicLevel,
		Development:      false,
		Encoding:         "console",
		EncoderConfig:    zap.NewProductionEncoderConfig(),
		OutputPaths:      []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
	}

	config.EncoderConfig.TimeKey = "timestamp"
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	config.EncoderConfig.LevelKey = "level"
	config.EncoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	config.EncoderConfig.CallerKey = "caller"
	config.EncoderConfig.EncodeCaller = zapcore.ShortCallerEncoder
	config.EncoderConfig.MessageKey = "message"
	config.EncoderConfig.StacktraceKey = ""

	log, err := config.Build()
	if err != nil {
		panic(err)
	}
	return log, atomicLevel
}

// Component is the logging setup of one network function: a zap logger with its own level, from which
// the per-category loggers are derived. Every record carries "component" and "category" fields.
type Component struct {
	log   *zap.Logger
	level zap.AtomicLevel
	name  string
}

// NewComponent returns the logging setup for the component with the given name, e.g. "UDM".
func NewComponent(name string) *Component {
	log, level := NewZap()
	return &Component{name: name, log: log, level: level}
}

// Category returns a logger whose records carry the component and the given category.
func (c *Component) Category(category string) *zap.SugaredLogger {
	return c.log.Sugar().With("component", c.name, "category", category)
}

// SetLevel changes the level of every logger derived from the component.
func (c *Component) SetLevel(level zapcore.Level) {
	c.level.SetLevel(level)
}

// Level returns the current level of the component's loggers.
func (c *Component) Level() zapcore.Level {
	return c.level.Level()
}

// Zap returns the underlying zap logger.
func (c *Component) Zap() *zap.Logger {
	return c.log
}
