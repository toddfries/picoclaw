package tools

import (
	"context"
	"strings"

	"github.com/sipeed/picoclaw/pkg/channels/pico"
)

// BusSendTool delivers one bundle to one endpoint over the Pico control
// socket. It waits for bus.ack or bus.error, not for the other model's reply.
type BusSendTool struct {
	workspace string
}

func NewBusSendTool(workspace string) *BusSendTool {
	return &BusSendTool{workspace: workspace}
}

func (t *BusSendTool) Name() string { return "bus_send" }

func (t *BusSendTool) Description() string {
	return "Send one bundle to one endpoint from peers. " +
		"to is an endpoint id, or a nick that matches exactly one id. " +
		"content is the text. Returns when the bus accepts or refuses the bundle. " +
		"The other endpoint's answer arrives later as its own turn. " +
		"A turn that arrived from the bus is answered by your normal reply; " +
		"do not send a bundle back to that sender. That turn may send at most one bundle."
}

func (t *BusSendTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"to": map[string]any{
				"type":        "string",
				"description": "Endpoint id, or a nick that matches exactly one id",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Bundle text",
			},
		},
		"required":             []string{"to", "content"},
		"additionalProperties": true,
	}
}

func (t *BusSendTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	to, _ := args["to"].(string)
	content, _ := args["content"].(string)
	msg, err := pico.SendBundle(ctx, t.workspace, ToolChatID(ctx), strings.TrimSpace(to), content)
	if err != nil {
		return ErrorResult(err.Error())
	}
	return SilentResult(msg)
}
