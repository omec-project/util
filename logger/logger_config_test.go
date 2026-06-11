// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
// SPDX-FileCopyrightText: 2024 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package logger

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestLogSettingValidateAllowsEmptyDebugLevel(t *testing.T) {
	valid, err := (&LogSetting{}).validate()
	if err != nil {
		t.Fatalf("validate() returned unexpected error: %v", err)
	}
	if !valid {
		t.Fatal("validate() returned false for empty debugLevel")
	}
}

func TestLogSettingValidateRejectsInvalidDebugLevel(t *testing.T) {
	valid, err := (&LogSetting{DebugLevel: "invalid"}).validate()
	if err == nil {
		t.Fatal("validate() returned nil error for invalid debugLevel")
	}
	if valid {
		t.Fatal("validate() returned true for invalid debugLevel")
	}
}

func TestApplyLogSettingDefaultsToInfoForEmptyDebugLevel(t *testing.T) {
	var gotLevel zapcore.Level

	ApplyLogSetting("Test", &LogSetting{}, zap.NewNop().Sugar(), func(level zapcore.Level) {
		gotLevel = level
	})

	if gotLevel != zap.InfoLevel {
		t.Fatalf("ApplyLogSetting() set level %s, want %s", gotLevel, zap.InfoLevel)
	}
}
