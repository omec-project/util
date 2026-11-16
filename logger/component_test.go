// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package logger

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestNewZap_StartsAtInfoAndLevelControlsTheLogger(t *testing.T) {
	log, level := NewZap()
	if level.Level() != zap.InfoLevel {
		t.Errorf("initial level = %v, want info", level.Level())
	}
	if log.Core().Enabled(zap.DebugLevel) {
		t.Error("debug must be disabled at info level")
	}
	level.SetLevel(zap.DebugLevel)
	if !log.Core().Enabled(zap.DebugLevel) {
		t.Error("changing the level did not reach the logger")
	}
}

func TestComponent_SetLevelAffectsAllCategories(t *testing.T) {
	c := NewComponent("UDM")
	app, init := c.Category("App"), c.Category("Init")
	if app.Desugar().Core().Enabled(zap.DebugLevel) {
		t.Fatal("debug must be disabled by default")
	}

	c.SetLevel(zapcore.DebugLevel)
	for name, l := range map[string]*zap.SugaredLogger{"App": app, "Init": init} {
		if !l.Desugar().Core().Enabled(zap.DebugLevel) {
			t.Errorf("category %s did not follow SetLevel", name)
		}
	}
	if c.Level() != zapcore.DebugLevel {
		t.Errorf("Level() = %v, want debug", c.Level())
	}
}

func TestComponent_LevelsAreIndependentBetweenComponents(t *testing.T) {
	a, b := NewComponent("UDM"), NewComponent("UDR")
	a.SetLevel(zapcore.DebugLevel)
	if b.Level() != zapcore.InfoLevel {
		t.Errorf("changing one component changed another: %v", b.Level())
	}
	if b.Category("App").Desugar().Core().Enabled(zap.DebugLevel) {
		t.Error("the other component's loggers must stay at info")
	}
}

func TestComponent_CategoryAddsComponentAndCategoryFields(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	c := &Component{name: "UDM", log: zap.New(core), level: zap.NewAtomicLevel()}

	c.Category("Init").Infoln("hello")

	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["component"] != "UDM" || fields["category"] != "Init" {
		t.Errorf("fields = %v, want component=UDM category=Init", fields)
	}
	if entries[0].Message != "hello" {
		t.Errorf("message = %q", entries[0].Message)
	}
}

func TestComponent_ZapReturnsTheUnderlyingLogger(t *testing.T) {
	if NewComponent("UDM").Zap() == nil {
		t.Error("Zap() returned nil")
	}
}
