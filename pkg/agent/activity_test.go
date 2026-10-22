package agent

import (
	"sync"
	"testing"
	"time"
)

func TestCrewActivityHeartbeatIgnoredAndOverlapEndsIdle(t *testing.T) {
	var mu sync.Mutex
	var events []string
	al := &AgentLoop{
		activityEvery: time.Hour,
		activityPublish: func(state string) {
			mu.Lock()
			events = append(events, state)
			mu.Unlock()
		},
	}

	al.noteCrewTurn(activityHeartbeatKey, true)
	al.noteCrewTurn(activityHeartbeatKey, false)
	al.noteCrewTurn("alpha", true)
	al.noteCrewTurn("beta", true)
	al.noteCrewTurn("alpha", false)

	mu.Lock()
	mid := append([]string(nil), events...)
	mu.Unlock()
	if len(mid) != 1 || mid[0] != activityThinking {
		t.Fatalf("while overlapped = %v, want [thinking]", mid)
	}

	al.noteCrewTurn("beta", false)
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0] != activityThinking || events[1] != activityIdle {
		t.Fatalf("events = %v, want [thinking idle]", events)
	}
}

func TestPublishActivityRechecksLiveTurns(t *testing.T) {
	var events []string
	al := &AgentLoop{
		activityPublish: func(state string) {
			events = append(events, state)
		},
	}
	al.publishActivity(activityThinking)
	al.liveTurns = 1
	al.publishActivity(activityIdle)
	al.liveTurns = 0
	al.publishActivity(activityIdle)
	if len(events) != 1 || events[0] != activityIdle {
		t.Fatalf("events = %v, want [idle] only when no turn is live", events)
	}
}

func TestCrewActivityPulseStopsAtIdle(t *testing.T) {
	var mu sync.Mutex
	var events []string
	al := &AgentLoop{
		activityEvery: 20 * time.Millisecond,
		activityPublish: func(state string) {
			mu.Lock()
			events = append(events, state)
			mu.Unlock()
		},
	}
	al.noteCrewTurn("alpha", true)
	deadline := time.Now().Add(300 * time.Millisecond)
	for {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	al.noteCrewTurn("alpha", false)
	time.Sleep(60 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(events) < 2 || events[len(events)-1] != activityIdle {
		t.Fatalf("events = %v, want thinking pulses then idle", events)
	}
	for i, ev := range events[:len(events)-1] {
		if ev != activityThinking {
			t.Fatalf("event %d = %s, want thinking before the final idle", i, ev)
		}
	}
	thinking := 0
	for _, ev := range events {
		if ev == activityThinking {
			thinking++
		}
	}
	if thinking < 2 {
		t.Fatalf("events = %v, want the pulse to repeat thinking", events)
	}
}
