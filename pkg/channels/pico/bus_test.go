package pico

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func testBusChannel(t *testing.T) *PicoChannel {
	t.Helper()
	bc := &config.Channel{Type: config.ChannelPico, Enabled: true}
	cfg := &config.PicoSettings{}
	cfg.SetToken("test-token")
	ch, err := NewPicoChannel(bc, cfg, bus.NewMessageBus())
	if err != nil {
		t.Fatalf("NewPicoChannel: %v", err)
	}
	ch.ctx = context.Background()
	ch.busSelf = "zhc"
	return ch
}

func TestDirectoryFrameDoesNotStartTurn(t *testing.T) {
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{Type: config.ChannelPico, Enabled: true}
	cfg := &config.PicoSettings{}
	cfg.SetToken("test-token")
	ch, err := NewPicoChannel(bc, cfg, msgBus)
	if err != nil {
		t.Fatal(err)
	}
	ch.ctx = context.Background()
	ws := t.TempDir()
	ch.SetBusWorkspace(ws)

	ch.handleMessage(nil, PicoMessage{
		Type: TypeDirectory,
		Payload: map[string]any{
			"digest": "abc",
			"endpoints": []any{
				map[string]any{"id": "zhc-ops", "nick": "ops", "role": "ops"},
			},
		},
	})

	body, err := os.ReadFile(DirectoryFile(ws))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"id": "zhc-ops"`) {
		t.Fatalf("directory = %s", body)
	}
	select {
	case inbound := <-msgBus.InboundChan():
		t.Fatalf("directory started a turn: %+v", inbound)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHopCap(t *testing.T) {
	ch := testBusChannel(t)
	chat := "pico:pb.zhc-ops"
	ch.noteBusTurn(chat, "[bus from=zhc-ops] hi")
	if msg := ch.rejectHop(chat, "zhc-ops"); msg == "" {
		t.Fatal("expected refuse back to sender")
	}
	if msg := ch.rejectHop(chat, "todd"); msg != "" {
		t.Fatalf("first forward refused: %s", msg)
	}
	ch.markHopSend(chat)
	if msg := ch.rejectHop(chat, "todd"); msg == "" {
		t.Fatal("expected one-bundle cap")
	}
	ch.noteBusTurn(chat, "hello from discord")
	if msg := ch.rejectHop(chat, "todd"); msg != "" {
		t.Fatalf("non-bus turn was capped: %s", msg)
	}
}

func TestResolveNick(t *testing.T) {
	ws := t.TempDir()
	body := []byte(`{"digest":"d","endpoints":[
		{"id":"zhc-ops","nick":"ops"},
		{"id":"zhc-3d","nick":"scout"},
		{"id":"todd","nick":"todd"}
	]}`)
	if err := os.MkdirAll(filepath.Join(ws, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(DirectoryFile(ws), body, 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := resolveBusTarget(ws, "ops")
	if err != nil || id != "zhc-ops" {
		t.Fatalf("ops → %q %v", id, err)
	}
	id, err = resolveBusTarget(ws, "zhc-ops")
	if err != nil || id != "zhc-ops" {
		t.Fatalf("id → %q %v", id, err)
	}
	if _, err := resolveBusTarget(ws, "all"); err == nil {
		t.Fatal("all should be refused")
	}

	amb := []byte(`{"endpoints":[{"id":"a","nick":"sam"},{"id":"b","nick":"sam"}]}`)
	if err := os.WriteFile(DirectoryFile(ws), amb, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveBusTarget(ws, "sam"); err == nil {
		t.Fatal("ambiguous nick should not send")
	}
}

func TestSendBundleWaitsForAck(t *testing.T) {
	ch := testBusChannel(t)
	ws := t.TempDir()
	ch.SetBusWorkspace(ws)
	ch.busSendFn = func(msg PicoMessage) error {
		if msg.Type != TypeBusSend || msg.SessionID != "pb.zhc" {
			t.Errorf("frame = %+v", msg)
		}
		go ch.handleMessage(nil, PicoMessage{
			Type:    TypeBusAck,
			ID:      msg.ID,
			Payload: map[string]any{"to": "todd"},
		})
		return nil
	}
	ch.bindCurrent()
	t.Cleanup(ch.unbindCurrent)

	got, err := SendBundle(context.Background(), ws, "pico:discord", "todd", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "accepted by todd") {
		t.Fatalf("result = %q", got)
	}
}

func TestSendBundleUnreachable(t *testing.T) {
	ch := testBusChannel(t)
	ch.busSendFn = func(msg PicoMessage) error {
		go ch.handleMessage(nil, PicoMessage{
			Type:    TypeBusError,
			ID:      msg.ID,
			Payload: map[string]any{"to": "zhc-ops", "error": "unreachable"},
		})
		return nil
	}
	ch.bindCurrent()
	t.Cleanup(ch.unbindCurrent)

	_, err := SendBundle(context.Background(), t.TempDir(), "", "zhc-ops", "hello")
	if err == nil || err.Error() != "unreachable" {
		t.Fatalf("err = %v", err)
	}
}

func TestSendBundleNoSocket(t *testing.T) {
	_, err := SendBundle(context.Background(), t.TempDir(), "", "zhc-ops", "hello")
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("err = %v", err)
	}
}
