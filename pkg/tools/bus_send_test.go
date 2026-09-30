package tools

import (
	"context"
	"strings"
	"testing"
)

func TestPeersEmptyDirectory(t *testing.T) {
	tool := NewPeersTool(t.TempDir())
	res := tool.Execute(context.Background(), map[string]any{})
	if res.IsError {
		t.Fatal(res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, `"endpoints":[]`) && !strings.Contains(res.ForLLM, `"endpoints": []`) {
		t.Fatalf("peers = %q", res.ForLLM)
	}
}

func TestBusSendRequiresArgs(t *testing.T) {
	tool := NewBusSendTool(t.TempDir())
	res := tool.Execute(context.Background(), map[string]any{"to": "zhc-ops"})
	if !res.IsError {
		t.Fatal("expected error for missing content")
	}
}

func TestBusSendWithoutChannel(t *testing.T) {
	tool := NewBusSendTool(t.TempDir())
	res := tool.Execute(context.Background(), map[string]any{
		"to":      "zhc-ops",
		"content": "hello",
	})
	if !res.IsError || !strings.Contains(res.ForLLM, "not connected") {
		t.Fatalf("result = %+v", res)
	}
}
