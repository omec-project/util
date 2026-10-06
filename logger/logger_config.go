// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-FileCopyrightText: 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package logger

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type Logger struct {
	AMF    *LogSetting `yaml:"AMF"`
	AUSF   *LogSetting `yaml:"AUSF"`
	N3IWF  *LogSetting `yaml:"N3IWF"`
	NRF    *LogSetting `yaml:"NRF"`
	NSSF   *LogSetting `yaml:"NSSF"`
	PCF    *LogSetting `yaml:"PCF"`
	SMF    *LogSetting `yaml:"SMF"`
	UDM    *LogSetting `yaml:"UDM"`
	UDR    *LogSetting `yaml:"UDR"`
	UPF    *LogSetting `yaml:"UPF"`
	NEF    *LogSetting `yaml:"NEF"`
	BSF    *LogSetting `yaml:"BSF"`
	CHF    *LogSetting `yaml:"CHF"`
	UDSF   *LogSetting `yaml:"UDSF"`
	NWDAF  *LogSetting `yaml:"NWDAF"`
	WEBUI  *LogSetting `yaml:"WEBUI"`
	SCTPLB *LogSetting `yaml:"SCTPLB"`

	Util                         *LogSetting `yaml:"Util"`
	NAS                          *LogSetting `yaml:"NAS"`
	NGAP                         *LogSetting `yaml:"NGAP"`
	OpenApi                      *LogSetting `yaml:"OpenApi"`
	NamfCommunication            *LogSetting `yaml:"NamfCommunication"`
	NamfEventExposure            *LogSetting `yaml:"NamfEventExposure"`
	NnssfNSSAIAvailability       *LogSetting `yaml:"NnssfNSSAIAvailability"`
	NnssfNSSelection             *LogSetting `yaml:"NnssfNSSelection"`
	NsmfEventExposure            *LogSetting `yaml:"NsmfEventExposure"`
	NsmfPDUSession               *LogSetting `yaml:"NsmfPDUSession"`
	NudmEventExposure            *LogSetting `yaml:"NudmEventExposure"`
	NudmParameterProvision       *LogSetting `yaml:"NudmParameterProvision"`
	NudmSubscriberDataManagement *LogSetting `yaml:"NudmSubscriberDataManagement"`
	NudmUEAuthentication         *LogSetting `yaml:"NudmUEAuthentication"`
	NudmUEContextManagement      *LogSetting `yaml:"NudmUEContextManagement"`
	NudrDataRepository           *LogSetting `yaml:"NudrDataRepository"`
}

type LogSetting struct {
	DebugLevel string `yaml:"debugLevel"`
}

func (l *LogSetting) validate() (bool, error) {
	if l == nil {
		return false, fmt.Errorf("log setting is nil")
	}

	if l.DebugLevel == "" {
		return true, nil
	}

	if !isValidDebugLevel(l.DebugLevel) {
		return false, fmt.Errorf("invalid debugLevel: %s", l.DebugLevel)
	}

	return true, nil
}

// isValidDebugLevel validates if the debug level is supported by zap
func isValidDebugLevel(level string) bool {
	_, err := zapcore.ParseLevel(level)
	return err == nil
}

// ApplyLogSetting sets a module logger level from config, defaulting to info.
func ApplyLogSetting(moduleName string, moduleCfg *LogSetting, logObj *zap.SugaredLogger, setLevel func(zapcore.Level)) {
	if moduleCfg == nil || moduleCfg.DebugLevel == "" {
		logObj.Warnf("%s log level not set; defaulting to [info] level", moduleName)
		setLevel(zap.InfoLevel)
		return
	}

	level, err := zapcore.ParseLevel(moduleCfg.DebugLevel)
	if err != nil {
		logObj.Warnf("%s log level [%s] is invalid; defaulting to [info] level", moduleName, moduleCfg.DebugLevel)
		setLevel(zap.InfoLevel)
		return
	}

	setLevel(level)
}
