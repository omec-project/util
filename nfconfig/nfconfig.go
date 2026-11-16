// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

// Package nfconfig holds the parts of loading a network function's YAML configuration that every
// network function does the same way: reading the file, the webconsole URI, and the config version.
package nfconfig

import (
	"fmt"
	"net/url"
	"os"
	"reflect"

	"go.yaml.in/yaml/v4"
)

// DefaultWebuiUri is the webconsole URI used when the configuration does not set one.
const DefaultWebuiUri = "http://webui:5001"

// Logger is the part of a (zap sugared) logger that this package uses.
type Logger interface {
	Infof(template string, args ...any)
}

// hasLogger reports whether log can be called: neither a nil interface nor an interface holding a nil
// pointer, such as a nil *zap.SugaredLogger.
func hasLogger(log Logger) bool {
	if log == nil {
		return false
	}
	v := reflect.ValueOf(log)
	return v.Kind() != reflect.Pointer || !v.IsNil()
}

// LoadYAML reads the YAML file at path into out, which must be a pointer. Callers that want a clean
// slate must zero the value first, fields absent from the file keep their current value.
func LoadYAML(path string, out any) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(content, out)
}

// ResolveWebuiUri returns the webconsole URI to use: DefaultWebuiUri (logged) if uri is empty, else uri
// if it is valid (see ValidateWebuiUri). log may be nil.
func ResolveWebuiUri(uri string, log Logger) (string, error) {
	if uri == "" {
		if hasLogger(log) {
			log.Infof("webuiUri not set in configuration file. Using %v", DefaultWebuiUri)
		}
		return DefaultWebuiUri, nil
	}
	if err := ValidateWebuiUri(uri); err != nil {
		return "", err
	}
	return uri, nil
}

// ValidateWebuiUri checks that uri is an absolute http or https URI with a host.
func ValidateWebuiUri(uri string) error {
	parsedUrl, err := url.ParseRequestURI(uri)
	if err != nil {
		return err
	}
	if parsedUrl.Scheme != "http" && parsedUrl.Scheme != "https" {
		return fmt.Errorf("unsupported scheme for webuiUri: %s", parsedUrl.Scheme)
	}
	if parsedUrl.Hostname() == "" {
		return fmt.Errorf("missing host in webuiUri")
	}
	return nil
}

// CheckVersion returns an error if the version of the configuration is not the expected one, and logs
// the version otherwise. log may be nil.
func CheckVersion(current, expected string, log Logger) error {
	if current != expected {
		return fmt.Errorf("config version is [%s], but expected is [%s]", current, expected)
	}
	if hasLogger(log) {
		log.Infof("config version [%s]", current)
	}
	return nil
}
