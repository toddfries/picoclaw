package agent

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestToolBudgetGrant(t *testing.T) {
	if n, ok := toolBudgetGrant("continue", 20); !ok || n != 40 {
		t.Fatalf("continue 20 = %d ok=%v, want 40", n, ok)
	}
	if n, ok := toolBudgetGrant("continue", 100); !ok || n != 140 {
		t.Fatalf("continue 100 = %d ok=%v, want 140", n, ok)
	}
	if n, ok := toolBudgetGrant("`raise to 80`", 20); !ok || n != 80 {
		t.Fatalf("raise = %d ok=%v, want 80", n, ok)
	}
	if n, ok := toolBudgetGrant("raise to 9000", 20); !ok || n != 200 {
		t.Fatalf("raise cap = %d ok=%v, want 200", n, ok)
	}
	if _, ok := toolBudgetGrant("commission brp", 20); ok {
		t.Fatal("ordinary text must not grant a budget")
	}
}

func TestFormatToolLimitStopRedactsAndLists(t *testing.T) {
	msg := formatToolLimitStop([]providers.Message{{
		Role: "assistant",
		ToolCalls: []providers.ToolCall{{
			Name: "read_file",
			Arguments: map[string]any{
				"path":  "/home/zhc-ops/.picoclaw/workspace/OPS-COMMISSION.md",
				"token": "sk-secret",
			},
		}, {
			Name:      "exec",
			Arguments: map[string]any{"command": "doas rcctl check zhc_ops_picoclaw"},
		}},
	}}, 2, 20, 20)
	if !strings.Contains(msg, "read_file /home/zhc-ops/.picoclaw/workspace/OPS-COMMISSION.md") {
		t.Fatalf("missing path: %s", msg)
	}
	if strings.Contains(msg, "sk-secret") {
		t.Fatalf("secret leaked: %s", msg)
	}
	if !strings.Contains(msg, "exec doas …") {
		t.Fatalf("missing command head: %s", msg)
	}
	if !strings.Contains(msg, "Reply `continue`") || !strings.Contains(msg, "Saved cap is 20") {
		t.Fatalf("missing grant instructions: %s", msg)
	}
}
