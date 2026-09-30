package providers

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers/common"
)

const (
	// Extra pause after the provider's own reset, then up to 20s of jitter.
	rateLimitCushion   = 30 * time.Second
	rateLimitJitterMax = 20
	// No usable header: the free-model minute window, not a 2s retry.
	rateLimitDefaultBase = 60 * time.Second
	rateLimitMaxDelay    = 26 * time.Hour
	// A reset this far out is the daily cap (or a long upstream ban),
	// not the per-minute window. Wait once, then one retry.
	rateLimitLongWait = 15 * time.Minute
	// Short windows get several backed-off tries before the prompt fails.
	rateLimitMaxHolds = 5
)

// RateLimitSnapshot is the limit state from one HTTP response.
type RateLimitSnapshot struct {
	Limit      string
	Remaining  string
	Reset      string
	RetryAfter string
	Message    string
	Platform   bool
	LongWait   bool
	Base       time.Duration
}

// RateLimitExhausted means this prompt already waited out a platform
// 429 and retried. Callers must not immediately send it again.
type RateLimitExhausted struct {
	Err      error
	Snapshot RateLimitSnapshot
}

func (e *RateLimitExhausted) Error() string {
	if e == nil || e.Err == nil {
		return "rate limit exhausted"
	}
	return e.Err.Error()
}

func (e *RateLimitExhausted) Unwrap() error { return e.Err }

// RateLimitGate is the process-wide "do not call again until" clock.
// One daemon's turns share it so a 429 backs the whole process off.
type RateLimitGate struct {
	mu        sync.Mutex
	notBefore time.Time
	Last      RateLimitSnapshot
}

func NewRateLimitGate() *RateLimitGate { return &RateLimitGate{} }

var processRateLimitGate = NewRateLimitGate()

func (g *RateLimitGate) Push(until time.Time, snap RateLimitSnapshot) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if until.After(g.notBefore) {
		g.notBefore = until
	}
	g.Last = snap
}

// Wait sleeps until the gate opens. now must advance across sleep or
// the single sleep is still only one wait (it does not loop).
func (g *RateLimitGate) Wait(
	ctx context.Context,
	now func() time.Time,
	sleep func(context.Context, time.Duration) error,
) error {
	if g == nil {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = sleepRateLimit
	}
	g.mu.Lock()
	until := g.notBefore
	g.mu.Unlock()
	n := now()
	if !until.After(n) {
		return nil
	}
	return sleep(ctx, until.Sub(n))
}

func sleepRateLimit(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func rateLimitSnapshot(err error) (RateLimitSnapshot, bool) {
	var api *common.APIError
	if !errors.As(err, &api) || api == nil || api.StatusCode != http.StatusTooManyRequests {
		return RateLimitSnapshot{}, false
	}
	return parseRateLimit(api.Headers, api.Body, time.Now()), true
}

func parseRateLimit(headers map[string]string, body []byte, now time.Time) RateLimitSnapshot {
	if headers == nil {
		headers = map[string]string{}
	}
	get := func(name string) string {
		if v, ok := headers[textproto.CanonicalMIMEHeaderKey(name)]; ok {
			return strings.TrimSpace(v)
		}
		if v, ok := headers[name]; ok {
			return strings.TrimSpace(v)
		}
		return ""
	}
	snap := RateLimitSnapshot{
		Limit:      get("X-RateLimit-Limit"),
		Remaining:  get("X-RateLimit-Remaining"),
		Reset:      get("X-RateLimit-Reset"),
		RetryAfter: get("Retry-After"),
		Message:    headers["X-Picoclaw-Rate-Message"],
	}
	if snap.Message == "" && len(body) > 0 {
		snap.Message = messageFromRateBody(body)
	}
	retryWait := parseRetryAfter(snap.RetryAfter, now)
	resetWait := parseReset(snap.Reset, now)
	snap.Base = retryWait
	if resetWait > snap.Base {
		snap.Base = resetWait
	}
	msg := strings.ToLower(snap.Message)
	snap.Platform = snap.Limit != "" ||
		strings.Contains(msg, "free-models-per-") ||
		strings.Contains(msg, "free_model")
	base := snap.Base
	if base <= 0 {
		base = rateLimitDefaultBase
	}
	snap.LongWait = base > rateLimitLongWait
	return snap
}

func (s RateLimitSnapshot) Delay(now time.Time, randN func(int) int) time.Duration {
	base := s.Base
	if base <= 0 {
		base = rateLimitDefaultBase
	}
	if base > rateLimitMaxDelay {
		base = rateLimitMaxDelay
	}
	jitter := 0
	if randN != nil && rateLimitJitterMax > 0 {
		jitter = randN(rateLimitJitterMax + 1)
		if jitter < 0 {
			jitter = 0
		}
	}
	return base + rateLimitCushion + time.Duration(jitter)*time.Second
}

func (s RateLimitSnapshot) maxHolds() int {
	if s.LongWait {
		return 1
	}
	return rateLimitMaxHolds
}

func messageFromRateBody(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Error.Message)
}

func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs * float64(time.Second))
	}
	if when, err := http.ParseTime(v); err == nil {
		d := when.Sub(now)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}

func parseReset(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if when, err := http.ParseTime(v); err == nil {
		d := when.Sub(now)
		if d < 0 {
			return 0
		}
		return d
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(v, 64)
		if ferr != nil {
			return 0
		}
		n = int64(f)
	}
	switch {
	case n >= 1_000_000_000_000:
		d := time.UnixMilli(n).Sub(now)
		if d < 0 {
			return 0
		}
		return d
	case n >= 1_000_000_000:
		d := time.Unix(n, 0).Sub(now)
		if d < 0 {
			return 0
		}
		return d
	case n <= 0:
		return 0
	default:
		return time.Duration(n) * time.Second
	}
}

func defaultRandN(n int) int {
	if n <= 0 {
		return 0
	}
	return rand.IntN(n)
}
