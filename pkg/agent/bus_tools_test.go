package agent

import "testing"

func TestBusToolsRegistered(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	agent := al.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent should exist")
	}
	for _, name := range []string{"peers", "bus_send"} {
		if _, has := agent.Tools.Get(name); !has {
			t.Errorf("missing tool %s", name)
		}
	}
	if _, has := agent.Tools.Get("delegate"); has {
		t.Error("delegate should not be registered")
	}
}
