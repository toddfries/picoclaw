// PicoClaw - Ultra-lightweight personal AI agent

package agent

import "time"

const (
	activityThinking     = "thinking"
	activityIdle         = "idle"
	activityHeartbeatKey = "heartbeat"
	defaultActivityEvery = 1500 * time.Millisecond
)

// noteCrewTurn publishes thinking while at least one real turn is running,
// and idle when the last one ends. A heartbeat session is not a crew turn.
// start is true at runTurn entry and false from its defer.
func (al *AgentLoop) noteCrewTurn(sessionKey string, start bool) {
	if al == nil || sessionKey == activityHeartbeatKey {
		return
	}
	if start {
		al.crewActivityStart()
		return
	}
	al.crewActivityStop()
}

func (al *AgentLoop) crewActivityStart() {
	al.activityMu.Lock()
	al.liveTurns++
	first := al.liveTurns == 1
	var stop chan struct{}
	every := al.activityEvery
	if first {
		stop = make(chan struct{})
		al.activityStop = stop
		if every <= 0 {
			every = defaultActivityEvery
		}
	}
	al.activityMu.Unlock()
	if !first {
		return
	}
	al.publishActivity(activityThinking)
	go al.pulseActivity(stop, every)
}

func (al *AgentLoop) crewActivityStop() {
	al.activityMu.Lock()
	if al.liveTurns == 0 {
		al.activityMu.Unlock()
		return
	}
	al.liveTurns--
	last := al.liveTurns == 0
	stop := al.activityStop
	if last {
		al.activityStop = nil
	}
	al.activityMu.Unlock()
	if !last {
		return
	}
	if stop != nil {
		close(stop)
	}
	al.publishActivity(activityIdle)
}

// pulseActivity repeats thinking so a long turn is not mistaken for a freeze.
// The hub drops thinking on its own if these updates stop.
func (al *AgentLoop) pulseActivity(stop chan struct{}, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			al.publishActivity(activityThinking)
		}
	}
}

// publishActivity rechecks liveTurns under activitySendMu so a pulse that
// lost the race cannot send thinking after idle, or idle after a new turn.
func (al *AgentLoop) publishActivity(state string) {
	al.activitySendMu.Lock()
	defer al.activitySendMu.Unlock()

	al.activityMu.Lock()
	live := al.liveTurns
	al.activityMu.Unlock()
	if state == activityThinking && live == 0 {
		return
	}
	if state == activityIdle && live > 0 {
		return
	}
	if al.activityPublish != nil {
		al.activityPublish(state)
		return
	}
	cm := al.channelManager
	if cm == nil {
		return
	}
	broadcaster, ok := cm.(interface{ BroadcastActivity(string) })
	if !ok || broadcaster == nil {
		return
	}
	broadcaster.BroadcastActivity(state)
}
