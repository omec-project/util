// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-License-Identifier: Apache-2.0

package httpwrapper

import (
	"context"
	"net/http"
	"testing"
)

func TestNewRequest(t *testing.T) {
	req, err := http.NewRequestWithContext(context.Background(),
		"GET", "http://localhost:8080?name=Aether&location=USA", nil)
	if err != nil {
		t.Errorf("TestNewRequest error: %+v", err)
	}
	req.Header.Set("Location", "https://aetherproject.org/")
	request := NewRequest(req, 1000)

	if got := request.Header.Get("Location"); got != "https://aetherproject.org/" {
		t.Errorf("Header.Get(\"Location\") = %q, want %q", got, "https://aetherproject.org/")
	}

	if got := request.Query.Get("name"); got != "Aether" {
		t.Errorf("Query.Get(\"name\") = %q, want %q", got, "Aether")
	}

	if got := request.Query.Get("location"); got != "USA" {
		t.Errorf("Query.Get(\"location\") = %q, want %q", got, "USA")
	}

	if got := request.Body; got != 1000 {
		t.Errorf("Body = %v, want %v", got, 1000)
	}
}

func TestNewResponse(t *testing.T) {
	response := NewResponse(http.StatusCreated, map[string][]string{
		"Location": {"https://aetherproject.org/"},
		"Refresh":  {"url=https://docs.sd-core.opennetworking.org"},
	}, 1000)

	if got := response.Header.Get("Location"); got != "https://aetherproject.org/" {
		t.Errorf("Header.Get(\"Location\") = %q, want %q", got, "https://aetherproject.org/")
	}

	if got := response.Header.Get("Refresh"); got != "url=https://docs.sd-core.opennetworking.org" {
		t.Errorf("Header.Get(\"Refresh\") = %q, want %q", got, "url=https://docs.sd-core.opennetworking.org")
	}

	if got := response.Body; got != 1000 {
		t.Errorf("Body = %v, want %v", got, 1000)
	}
}
