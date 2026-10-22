package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/sipeed/picoclaw/pkg/providers"
)

const toolBudgetLedgerTail = 30

// toolBudgetGrant is a one-turn cap. The saved config value is left alone.
// continue grants one more window, at most 40 steps, on top of the saved cap.
// "raise to N" uses N for this turn only, and N stops at 200.
func toolBudgetGrant(text string, saved int) (turnCap int, ok bool) {
	if saved < 1 {
		saved = 20
	}
	s := strings.ToLower(strings.TrimSpace(text))
	s = strings.Trim(s, "`*_")
	s = strings.TrimSpace(s)
	switch s {
	case "continue", "more tools", "reset tools", "reset tool limit", "another window":
		extra := saved
		if extra > 40 {
			extra = 40
		}
		return saved + extra, true
	}
	rest, found := strings.CutPrefix(s, "raise to ")
	if !found {
		return saved, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil || n < 1 {
		return saved, false
	}
	if n > 200 {
		n = 200
	}
	return n, true
}

func formatToolLimitStop(messages []providers.Message, used, savedCap, turnCap int) string {
	var steps []string
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			line := tc.Name
			if line == "" {
				line = "tool"
			}
			if target := toolBudgetTarget(tc.Arguments); target != "" {
				line += " " + target
			}
			steps = append(steps, line)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Stopped after %d tool steps. Saved cap is %d. This turn's window was %d.\n",
		used, savedCap, turnCap)
	if len(steps) == 0 {
		b.WriteString("No tool call was recorded on this turn.\n")
	} else {
		start := 0
		if len(steps) > toolBudgetLedgerTail {
			fmt.Fprintf(&b, "Showing the last %d of %d steps.\n", toolBudgetLedgerTail, len(steps))
			start = len(steps) - toolBudgetLedgerTail
		}
		for i := start; i < len(steps); i++ {
			fmt.Fprintf(&b, "%d. %s\n", i+1, steps[i])
		}
	}
	b.WriteString("Reply `continue` for another window on this task (at most 40 extra steps). ")
	b.WriteString("Reply `raise to N` to use N steps on the next turn (N stops at 200). ")
	b.WriteString("The number in config.json stays where it is.\n")
	return b.String()
}

func toolBudgetSecretKey(key string) bool {
	k := strings.ToLower(key)
	switch {
	case strings.Contains(k, "token"),
		strings.Contains(k, "secret"),
		strings.Contains(k, "password"),
		strings.Contains(k, "authorization"),
		k == "key",
		strings.Contains(k, "api_key"),
		strings.Contains(k, "apikey"):
		return true
	default:
		return false
	}
}

func toolBudgetTarget(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	for _, key := range []string{"path", "file", "command", "cmd", "query", "pattern", "url", "uri"} {
		if v, ok := args[key]; ok {
			if s := toolBudgetScalar(key, v); s != "" {
				return s
			}
		}
	}
	for key, v := range args {
		if toolBudgetSecretKey(key) {
			continue
		}
		if s := toolBudgetScalar(key, v); s != "" {
			return key + "=" + s
		}
	}
	return ""
}

func toolBudgetScalar(key string, v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	s = strings.TrimSpace(s)
	if s == "" || toolBudgetSecretKey(key) {
		return ""
	}
	lower := strings.ToLower(s)
	if strings.Contains(lower, "sk-") || strings.Contains(lower, "bearer ") || strings.Contains(lower, "api_key") {
		return "[redacted]"
	}
	if key == "command" || key == "cmd" {
		if i := strings.IndexAny(s, "\n\r"); i >= 0 {
			s = s[:i]
		}
		fields := strings.Fields(s)
		if len(fields) == 0 {
			return ""
		}
		s = fields[0]
		if len(fields) > 1 {
			s += " …"
		}
	}
	return toolBudgetTrim(s, 80)
}

func toolBudgetTrim(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func (al *AgentLoop) publishPicoToolStep(ctx context.Context, ts *turnState, iteration int, toolName string, args map[string]any) {
	if al == nil || ts == nil || ts.channel != "pico" || al.bus == nil {
		return
	}
	if toolName == "" {
		toolName = "tool"
	}
	line := fmt.Sprintf("%d. %s", iteration, toolName)
	if target := toolBudgetTarget(args); target != "" {
		line += " " + target
	}
	fbCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = al.bus.PublishOutbound(fbCtx, outboundMessageForTurnWithOptions(
		ts,
		line,
		outboundTurnMessageOptions{kind: messageKindToolFeedback},
	))
}
