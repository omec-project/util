// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package nfconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingLogger struct{ msgs []string }

func (l *recordingLogger) Infof(template string, args ...any) {
	l.msgs = append(l.msgs, fmt.Sprintf(template, args...))
}

func TestValidateWebuiUri(t *testing.T) {
	cases := []struct {
		uri     string
		wantErr string // substring, empty for valid
	}{
		{"http://webui:5001", ""},
		{"https://webui.example.com", ""},
		{"http://10.0.0.1:5001/base", ""},
		{"ftp://webui:5001", "unsupported scheme"},
		{"webui:5001", "unsupported scheme"}, // parses with scheme "webui"
		{"http://", "missing host"},
		{"http://:5001", "missing host"},
	}
	for _, tc := range cases {
		t.Run(tc.uri, func(t *testing.T) {
			err := ValidateWebuiUri(tc.uri)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("err = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
	if err := ValidateWebuiUri("relative/path"); err == nil {
		t.Error("a relative reference must be rejected")
	}
	if err := ValidateWebuiUri(""); err == nil {
		t.Error("an empty URI must be rejected by ValidateWebuiUri itself")
	}
}

func TestResolveWebuiUri(t *testing.T) {
	t.Run("empty uses the default and logs it", func(t *testing.T) {
		log := &recordingLogger{}
		got, err := ResolveWebuiUri("", log)
		if err != nil || got != DefaultWebuiUri {
			t.Fatalf("got %q, %v", got, err)
		}
		if len(log.msgs) != 1 || !strings.Contains(log.msgs[0], DefaultWebuiUri) {
			t.Errorf("log = %q", log.msgs)
		}
	})
	t.Run("a valid uri is kept and not logged", func(t *testing.T) {
		log := &recordingLogger{}
		got, err := ResolveWebuiUri("https://webui:9000", log)
		if err != nil || got != "https://webui:9000" || len(log.msgs) != 0 {
			t.Errorf("got %q, %v, log %q", got, err, log.msgs)
		}
	})
	t.Run("an invalid uri is an error", func(t *testing.T) {
		if got, err := ResolveWebuiUri("ftp://webui", nil); err == nil || got != "" {
			t.Errorf("got %q, %v", got, err)
		}
	})
	t.Run("nil logger is fine", func(t *testing.T) {
		if got, err := ResolveWebuiUri("", nil); err != nil || got != DefaultWebuiUri {
			t.Errorf("got %q, %v", got, err)
		}
	})
}

func TestCheckVersion(t *testing.T) {
	log := &recordingLogger{}
	if err := CheckVersion("1.0.0", "1.0.0", log); err != nil {
		t.Fatal(err)
	}
	if len(log.msgs) != 1 || !strings.Contains(log.msgs[0], "1.0.0") {
		t.Errorf("log = %q", log.msgs)
	}

	err := CheckVersion("0.9.0", "1.0.0", log)
	if err == nil || !strings.Contains(err.Error(), "[0.9.0]") || !strings.Contains(err.Error(), "[1.0.0]") {
		t.Errorf("err = %v, want both versions in the message", err)
	}
	if len(log.msgs) != 1 {
		t.Error("a mismatch must not log the version as accepted")
	}
	if err := CheckVersion("1.0.0", "1.0.0", nil); err != nil {
		t.Errorf("nil logger: %v", err)
	}
}

type sample struct {
	Info struct {
		Version string `yaml:"version"`
	} `yaml:"info"`
	Configuration struct {
		WebuiUri string `yaml:"webuiUri"`
		Port     int    `yaml:"port"`
	} `yaml:"configuration"`
}

func TestLoadYAML(t *testing.T) {
	write := func(content string) string {
		p := filepath.Join(t.TempDir(), "cfg.yaml")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("reads fields", func(t *testing.T) {
		var cfg sample
		if err := LoadYAML(write("info:\n  version: 1.0.0\nconfiguration:\n  webuiUri: http://w:1\n  port: 8080\n"), &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.Info.Version != "1.0.0" || cfg.Configuration.WebuiUri != "http://w:1" || cfg.Configuration.Port != 8080 {
			t.Errorf("cfg = %+v", cfg)
		}
	})
	t.Run("absent fields keep their value", func(t *testing.T) {
		cfg := sample{}
		cfg.Configuration.Port = 99
		if err := LoadYAML(write("info:\n  version: 2\n"), &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.Configuration.Port != 99 {
			t.Errorf("port = %d, want the pre-set 99 to survive", cfg.Configuration.Port)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if err := LoadYAML(filepath.Join(t.TempDir(), "nope.yaml"), &sample{}); !os.IsNotExist(err) {
			t.Errorf("err = %v, want a not-exist error", err)
		}
	})
	t.Run("invalid yaml", func(t *testing.T) {
		if err := LoadYAML(write("info: [unterminated"), &sample{}); err == nil {
			t.Error("expected a parse error")
		}
	})
}

type infoLogger struct{}

func (*infoLogger) Infof(string, ...any) {}

func TestTypedNilLoggerIsSafe(t *testing.T) {
	var l *infoLogger // typed nil: a non-nil Logger interface holding a nil pointer
	if _, err := ResolveWebuiUri("", l); err != nil {
		t.Fatal(err)
	}
	if err := CheckVersion("1", "1", l); err != nil {
		t.Fatal(err)
	}
	if hasLogger(l) || hasLogger(nil) || !hasLogger(&infoLogger{}) {
		t.Error("hasLogger misclassifies nil, typed nil or a real logger")
	}
}
