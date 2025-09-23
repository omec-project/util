// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-FileCopyrightText: 2025 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package logger

import (
	"fmt"
	"reflect"

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
	MongoDBLibrary               *LogSetting `yaml:"MongoDBLibrary"`
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

func (l *Logger) Validate() (bool, error) {
	if l == nil {
		return false, fmt.Errorf("logger is nil")
	}

	logger := reflect.ValueOf(l).Elem()
	loggerType := reflect.TypeOf(l).Elem()

	for i := 0; i < logger.NumField(); i++ {
		field := logger.Field(i)
		fieldType := loggerType.Field(i)

		// Skip unexported fields
		if !field.CanInterface() {
			continue
		}

		if field.Kind() == reflect.Ptr && !field.IsNil() {
			if logSetting, ok := field.Interface().(*LogSetting); ok && logSetting != nil {
				if valid, err := logSetting.validate(); !valid {
					return false, fmt.Errorf("validation failed for field %s: %w", fieldType.Name, err)
				}
			}
		}
	}

	return true, nil
}

type LogSetting struct {
	DebugLevel string `yaml:"debugLevel"`
}

func (l *LogSetting) validate() (bool, error) {
	if l == nil {
		return false, fmt.Errorf("log setting is nil")
	}

	if l.DebugLevel == "" {
		return false, fmt.Errorf("debugLevel cannot be empty")
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

// GetLogSettingName returns the field name for a given LogSetting pointer
func GetLogSettingName(logger *Logger, target *LogSetting) (string, error) {
	if logger == nil {
		return "", fmt.Errorf("logger is nil")
	}

	if target == nil {
		return "", fmt.Errorf("target LogSetting is nil")
	}

	loggerValue := reflect.ValueOf(logger).Elem()
	loggerType := reflect.TypeOf(logger).Elem()
	logSettingType := reflect.TypeOf((*LogSetting)(nil))

	for i := 0; i < loggerValue.NumField(); i++ {
		field := loggerValue.Field(i)
		fieldType := loggerType.Field(i)

		// Check if the field is of type *LogSetting and matches target
		if fieldType.Type == logSettingType && !field.IsNil() {
			if field.Interface().(*LogSetting) == target {
				return fieldType.Name, nil
			}
		}
	}

	return "", fmt.Errorf("LogSetting not found in logger")
}
