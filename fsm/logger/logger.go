// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-License-Identifier: Apache-2.0

package logger

import (
	"time"

	formatter "github.com/antonfisher/nested-logrus-formatter"
	"github.com/sirupsen/logrus"
)

var (
	log    *logrus.Logger
	FsmLog *logrus.Entry
)

func init() {
	log = logrus.New()
	log.SetReportCaller(false)

	log.Formatter = &formatter.Formatter{
		TimestampFormat: time.RFC3339,
		TrimMessages:    true,
		NoFieldsSpace:   true,
		HideKeys:        true,
		FieldsOrder:     []string{"component", "category"},
	}

	FsmLog = log.WithFields(logrus.Fields{"component": "LIB", "category": "FSM"})
}

func GetLogger() *logrus.Logger {
	return log
}

func SetLogLevel(level logrus.Level) {
	FsmLog.Infoln("set log level :", level)
	log.SetLevel(level)
}

func SetReportCaller(enable bool) {
	FsmLog.Infoln("set report call :", enable)
	log.SetReportCaller(enable)
}
