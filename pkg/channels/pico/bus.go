package pico

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const busAckTimeout = 5 * time.Second

var busFromRe = regexp.MustCompile(`^\[bus from=([^\]\s]+)\]`)

type busHop struct {
	from  string
	sends int
}

type busEndpoint struct {
	ID   string `json:"id"`
	Nick string `json:"nick"`
}

var (
	currentMu sync.Mutex
	currentCh *PicoChannel
)

func (c *PicoChannel) bindCurrent() {
	currentMu.Lock()
	currentCh = c
	currentMu.Unlock()
}

func (c *PicoChannel) unbindCurrent() {
	currentMu.Lock()
	if currentCh == c {
		currentCh = nil
	}
	currentMu.Unlock()
}

func currentChannel() *PicoChannel {
	currentMu.Lock()
	defer currentMu.Unlock()
	return currentCh
}

// SetBusWorkspace is the directory whose memory/bus-directory.json is updated
// when a directory frame arrives. It is the gateway default workspace.
func (c *PicoChannel) SetBusWorkspace(dir string) {
	if c == nil {
		return
	}
	c.busWorkspace = dir
}

func (c *PicoChannel) selfID() string {
	if c != nil && c.busSelf != "" {
		return c.busSelf
	}
	u, err := user.Current()
	if err != nil || u == nil {
		return ""
	}
	return u.Username
}

// DirectoryFile is workspace/memory/bus-directory.json.
func DirectoryFile(workspace string) string {
	if strings.TrimSpace(workspace) == "" {
		return ""
	}
	return filepath.Join(workspace, "memory", "bus-directory.json")
}

// ReadDirectory returns the stored directory. A missing file is an empty
// directory, not an error.
func ReadDirectory(workspace string) (string, error) {
	path := DirectoryFile(workspace)
	if path == "" {
		return `{"digest":"","endpoints":[]}`, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return `{"digest":"","endpoints":[]}`, nil
		}
		return "", err
	}
	if strings.TrimSpace(string(body)) == "" {
		return `{"digest":"","endpoints":[]}`, nil
	}
	return string(body), nil
}

func (c *PicoChannel) handleDirectory(msg PicoMessage) {
	if c == nil {
		return
	}
	path := DirectoryFile(c.busWorkspace)
	if path == "" {
		logger.WarnC("pico", "directory frame dropped: no workspace")
		return
	}
	payload := msg.Payload
	if payload == nil {
		payload = map[string]any{"digest": "", "endpoints": []any{}}
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		logger.ErrorCF("pico", "directory encode failed", map[string]any{"error": err.Error()})
		return
	}
	body = append(body, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logger.ErrorCF("pico", "directory mkdir failed", map[string]any{"error": err.Error()})
		return
	}
	if err := fileutil.WriteFileAtomic(path, body, 0o644); err != nil {
		logger.ErrorCF("pico", "directory write failed", map[string]any{"error": err.Error()})
		return
	}
	logger.InfoCF("pico", "stored bus directory", map[string]any{"path": path})
}

func (c *PicoChannel) noteBusTurn(chatID, content string) {
	if c == nil || chatID == "" {
		return
	}
	c.busMu.Lock()
	defer c.busMu.Unlock()
	if c.hops == nil {
		c.hops = map[string]*busHop{}
	}
	m := busFromRe.FindStringSubmatch(strings.TrimSpace(content))
	if m == nil {
		delete(c.hops, chatID)
		return
	}
	c.hops[chatID] = &busHop{from: m[1]}
}

func (c *PicoChannel) rejectHop(chatID, to string) string {
	if c == nil {
		return ""
	}
	c.busMu.Lock()
	defer c.busMu.Unlock()
	h := c.hops[chatID]
	if h == nil {
		return ""
	}
	if to == h.from {
		return "this turn arrived from that endpoint; answer it directly"
	}
	if h.sends >= 1 {
		return "this turn already sent one bundle"
	}
	return ""
}

func (c *PicoChannel) markHopSend(chatID string) {
	if c == nil {
		return
	}
	c.busMu.Lock()
	defer c.busMu.Unlock()
	if h := c.hops[chatID]; h != nil {
		h.sends++
	}
}

func (c *PicoChannel) deliverBusReply(msg PicoMessage) {
	if c == nil || msg.ID == "" {
		return
	}
	c.busMu.Lock()
	ch := c.busWaiters[msg.ID]
	c.busMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- msg:
	default:
	}
}

func loadBusEndpoints(workspace string) ([]busEndpoint, error) {
	path := DirectoryFile(workspace)
	if path == "" {
		return nil, os.ErrNotExist
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Endpoints []busEndpoint `json:"endpoints"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	return doc.Endpoints, nil
}

func resolveBusTarget(workspace, to string) (string, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return "", fmt.Errorf("to is required")
	}
	if to == "all" {
		return "", fmt.Errorf("to must be one endpoint")
	}
	eps, err := loadBusEndpoints(workspace)
	if err != nil {
		if os.IsNotExist(err) {
			return to, nil
		}
		return "", err
	}
	var nicks []string
	for _, ep := range eps {
		if ep.ID == to {
			return ep.ID, nil
		}
		if ep.Nick != "" && ep.Nick == to {
			nicks = append(nicks, ep.ID)
		}
	}
	switch len(nicks) {
	case 0:
		return to, nil
	case 1:
		return nicks[0], nil
	default:
		return "", fmt.Errorf("nick %q matches more than one endpoint", to)
	}
}

// SendBundle writes one bus.send on the control socket and waits for
// bus.ack or bus.error. It does not wait for the other endpoint's reply.
func SendBundle(ctx context.Context, workspace, chatID, to, content string) (string, error) {
	to = strings.TrimSpace(to)
	content = strings.TrimSpace(content)
	if to == "" || content == "" {
		return "", fmt.Errorf("to and content are required")
	}
	id, err := resolveBusTarget(workspace, to)
	if err != nil {
		return "", err
	}
	ch := currentChannel()
	if ch == nil {
		return "", fmt.Errorf("bus is not connected")
	}
	self := ch.selfID()
	if self == "" {
		return "", fmt.Errorf("cannot determine endpoint id")
	}
	if id == self {
		return "", fmt.Errorf("cannot send a bundle to yourself")
	}
	if msg := ch.rejectHop(chatID, id); msg != "" {
		return "", fmt.Errorf("%s", msg)
	}

	mid := uuid.NewString()
	wait := make(chan PicoMessage, 1)
	ch.busMu.Lock()
	if ch.busWaiters == nil {
		ch.busWaiters = map[string]chan PicoMessage{}
	}
	ch.busWaiters[mid] = wait
	ch.busMu.Unlock()
	defer func() {
		ch.busMu.Lock()
		delete(ch.busWaiters, mid)
		ch.busMu.Unlock()
	}()

	msg := newMessage(TypeBusSend, map[string]any{
		"to":      id,
		"content": content,
	})
	msg.ID = mid
	sid := "pb." + self
	msg.SessionID = sid

	if ch.busSendFn != nil {
		if err := ch.busSendFn(msg); err != nil {
			return "", fmt.Errorf("control socket down: %w", err)
		}
	} else if err := ch.broadcastToSession("pico:"+sid, msg); err != nil {
		return "", fmt.Errorf("control socket down: %w", err)
	}
	ch.markHopSend(chatID)

	timer := time.NewTimer(busAckTimeout)
	defer timer.Stop()
	select {
	case ack := <-wait:
		if ack.Type == TypeBusError {
			errText, _ := ack.Payload["error"].(string)
			if errText == "" {
				errText = "bus error"
			}
			return "", fmt.Errorf("%s", errText)
		}
		return "accepted by " + id + ". Their reply arrives as a later turn.", nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return "", fmt.Errorf("bus ack timeout")
	}
}
