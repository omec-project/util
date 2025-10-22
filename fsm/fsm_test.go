// Copyright 2019 Communication Service/Software Laboratory, National Chiao Tung University (free5gc.org)
//
// SPDX-License-Identifier: Apache-2.0

package fsm

import (
	"context"
	"fmt"
	"testing"
)

const (
	Opened StateType = "Opened"
	Closed StateType = "Closed"
)

const (
	Open  EventType = "Open"
	Close EventType = "Close"
)

func TestState(t *testing.T) {
	s := NewState(Closed)

	if s.Current() != Closed {
		t.Errorf("Current() failed: expected %v, got %v", Closed, s.Current())
	}

	if !s.Is(Closed) {
		t.Error("Is() failed: expected true for Closed state")
	}

	s.Set(Opened)

	if s.Current() != Opened {
		t.Errorf("Current() failed: expected %v, got %v", Opened, s.Current())
	}

	if !s.Is(Opened) {
		t.Error("Is() failed: expected true for Opened state")
	}
}

func TestFSM(t *testing.T) {
	ctx := context.Background()
	f, err := NewFSM(Transitions{
		{Event: Open, From: Closed, To: Opened},
		{Event: Close, From: Opened, To: Closed},
		{Event: Open, From: Opened, To: Opened},
		{Event: Close, From: Closed, To: Closed},
	}, Callbacks{
		Opened: func(ctx context.Context, state *State, event EventType, args ArgsType) {
			fmt.Printf("event [%+v] at state [%+v]\n", event, state.Current())
		},
		Closed: func(ctx context.Context, state *State, event EventType, args ArgsType) {
			fmt.Printf("event [%+v] at state [%+v]\n", event, state.Current())
		},
	})

	s := NewState(Closed)

	if err != nil {
		t.Errorf("NewFSM() failed: expected nil error, got %v", err)
	}

	if err := f.SendEvent(ctx, s, Open, ArgsType{"TestArg": "test arg"}); err != nil {
		t.Errorf("SendEvent() failed: expected nil error, got %v", err)
	}

	if err := f.SendEvent(ctx, s, Close, ArgsType{"TestArg": "test arg"}); err != nil {
		t.Errorf("SendEvent() failed: expected nil error, got %v", err)
	}

	if !s.Is(Closed) {
		t.Error("Transition failed: expected state to be Closed")
	}

	fakeEvent := EventType("fake event")
	expectedError := fmt.Sprintf("unknown transition[From: %s, Event: %s]", s.Current(), fakeEvent)
	if err := f.SendEvent(ctx, s, fakeEvent, nil); err == nil {
		t.Error("SendEvent() should have failed with fake event")
	} else if err.Error() != expectedError {
		t.Errorf("SendEvent() error mismatch: expected %q, got %q", expectedError, err.Error())
	}
}

func TestFSMInitFail(t *testing.T) {
	duplicateTrans := Transition{
		Event: Close, From: Opened, To: Closed,
	}
	_, err := NewFSM(Transitions{
		{Event: Open, From: Closed, To: Opened},
		duplicateTrans,
		duplicateTrans,
		{Event: Open, From: Opened, To: Opened},
		{Event: Close, From: Closed, To: Closed},
	}, Callbacks{
		Opened: func(ctx context.Context, state *State, event EventType, args ArgsType) {
			fmt.Printf("event [%+v] at state [%+v]\n", event, state.Current())
		},
		Closed: func(ctx context.Context, state *State, event EventType, args ArgsType) {
			fmt.Printf("event [%+v] at state [%+v]\n", event, state.Current())
		},
	})

	expectedError := fmt.Sprintf("duplicate transition: %+v", duplicateTrans)
	if err == nil {
		t.Error("NewFSM() should have failed with duplicate transition")
	} else if err.Error() != expectedError {
		t.Errorf("NewFSM() error mismatch: expected %q, got %q", expectedError, err.Error())
	}

	fakeState := StateType("fake state")

	_, err = NewFSM(Transitions{
		{Event: Open, From: Closed, To: Opened},
		{Event: Close, From: Opened, To: Closed},
		{Event: Open, From: Opened, To: Opened},
		{Event: Close, From: Closed, To: Closed},
	}, Callbacks{
		Opened: func(ctx context.Context, state *State, event EventType, args ArgsType) {
			fmt.Printf("event [%+v] at state [%+v]\n", event, state.Current())
		},
		Closed: func(ctx context.Context, state *State, event EventType, args ArgsType) {
			fmt.Printf("event [%+v] at state [%+v]\n", event, state.Current())
		},
		fakeState: func(ctx context.Context, state *State, event EventType, args ArgsType) {
			fmt.Printf("event [%+v] at state [%+v]\n", event, state.Current())
		},
	})

	expectedError = fmt.Sprintf("unknown state: %+v", fakeState)
	if err == nil {
		t.Error("NewFSM() should have failed with unknown state")
	} else if err.Error() != expectedError {
		t.Errorf("NewFSM() error mismatch: expected %q, got %q", expectedError, err.Error())
	}
}
