package tools

import (
	"context"
	"strings"

	"github.com/sipeed/picoclaw/pkg/channels/pico"
)

// PeersTool reads the stored bus directory. It does not probe the network
// and it does not start a turn on another endpoint.
type PeersTool struct {
	workspace string
}

func NewPeersTool(workspace string) *PeersTool {
	return &PeersTool{workspace: workspace}
}

func (t *PeersTool) Name() string { return "peers" }

func (t *PeersTool) Description() string {
	return "Read the bus directory of endpoints you can address. " +
		"Call this before bus_send. Returns digest and endpoints " +
		"(id, nick, role, presence). Nick is empty until that endpoint " +
		"publishes a beacon. Presence down means the endpoint exists but " +
		"is not reachable. Address an endpoint by id, never by pid."
}

func (t *PeersTool) Parameters() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": true,
	}
}

func (t *PeersTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	body, err := pico.ReadDirectory(t.workspace)
	if err != nil {
		return ErrorResult(err.Error())
	}
	return SilentResult(strings.TrimSpace(body))
}
