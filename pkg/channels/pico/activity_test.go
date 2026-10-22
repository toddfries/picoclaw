package pico

import (
	"encoding/json"
	"testing"
)

func TestActivityMessageJSON(t *testing.T) {
	msg, ok := activityMessage("thinking")
	if !ok {
		t.Fatal("thinking should be accepted")
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Type    string `json:"type"`
		Payload struct {
			State string `json:"state"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != TypeActivity || got.Payload.State != "thinking" {
		t.Fatalf("json = %s", raw)
	}
	if _, ok := activityMessage("spinning"); ok {
		t.Fatal("unknown state should be rejected")
	}
	if _, ok := activityMessage(""); ok {
		t.Fatal("empty state should be rejected")
	}
}

func TestBroadcastActivityEmptyDoesNotPanic(t *testing.T) {
	ch := &PicoChannel{}
	ch.BroadcastActivity("thinking")
	ch.BroadcastActivity("idle")
	ch.BroadcastActivity("spinning")
	ch.BroadcastActivity("")
}
