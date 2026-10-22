package providers

import (
	"context"
	"errors"
	"net/textproto"
	"strconv"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers/common"
)

func TestRateLimitDelayAddsCushionAndJitter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	snap := parseRateLimit(map[string]string{
		textproto.CanonicalMIMEHeaderKey("Retry-After"): "10",
	}, nil, now)
	got := snap.Delay(now, func(int) int { return 7 })
	want := 10*time.Second + 30*time.Second + 7*time.Second
	if got != want {
		t.Fatalf("delay %s, want %s", got, want)
	}
}

func TestRateLimitResetMillisecondsEpoch(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	reset := now.Add(2 * time.Minute).UnixMilli()
	snap := parseRateLimit(map[string]string{
		textproto.CanonicalMIMEHeaderKey("X-RateLimit-Reset"): strconv.FormatInt(reset, 10),
		textproto.CanonicalMIMEHeaderKey("X-RateLimit-Limit"): "20",
	}, nil, now)
	if snap.Base < 2*time.Minute-time.Second || snap.Base > 2*time.Minute+time.Second {
		t.Fatalf("base %s, want about 2m", snap.Base)
	}
	if !snap.Platform {
		t.Fatal("X-RateLimit-Limit should mark a platform limit")
	}
	if snap.LongWait {
		t.Fatal("2m reset is not the daily cap")
	}
}

func TestRateLimitDailyResetIsLongWait(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	snap := parseRateLimit(map[string]string{
		textproto.CanonicalMIMEHeaderKey("X-RateLimit-Reset"): strconv.FormatInt(now.Add(20*time.Hour).Unix(), 10),
		"X-Picoclaw-Rate-Message":                             "Rate limit exceeded: free-models-per-day.",
	}, nil, now)
	if !snap.LongWait {
		t.Fatalf("base %s should be a long wait", snap.Base)
	}
	if snap.maxHolds() != 1 {
		t.Fatalf("holds %d, want 1", snap.maxHolds())
	}
	if !snap.Platform {
		t.Fatal("free-models-per-day is a platform limit")
	}
}

func TestRateLimitMissingHeadersUsesMinuteWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	snap := parseRateLimit(nil, nil, now)
	got := snap.Delay(now, func(int) int { return 0 })
	if got != 60*time.Second+30*time.Second {
		t.Fatalf("delay %s, want 90s", got)
	}
}

func TestExecuteCandidateWaitsOutPlatform429(t *testing.T) {
	fc := NewFallbackChain(NewCooldownTracker(), nil)
	fc.gate = NewRateLimitGate()
	fc.randN = func(int) int { return 0 }
	now := time.Unix(1_700_000_000, 0)
	fc.now = func() time.Time { return now }
	var slept []time.Duration
	fc.sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		now = now.Add(d)
		return nil
	}

	calls := map[string]int{}
	cands := []FallbackCandidate{
		{Provider: "openrouter", Model: "nvidia/nemotron:free"},
		{Provider: "openrouter", Model: "other:free"},
	}
	resp, err := fc.ExecuteCandidate(context.Background(), cands, func(_ context.Context, c FallbackCandidate) (*LLMResponse, error) {
		calls[c.Model]++
		if c.Model == "nvidia/nemotron:free" {
			return nil, &common.APIError{
				StatusCode:  429,
				BodyPreview: `{"error":{"message":"Rate limit exceeded: free-models-per-min. "}}`,
				Headers: map[string]string{
					textproto.CanonicalMIMEHeaderKey("Retry-After"):           "10",
					textproto.CanonicalMIMEHeaderKey("X-RateLimit-Limit"):     "20",
					textproto.CanonicalMIMEHeaderKey("X-RateLimit-Remaining"): "0",
					"X-Picoclaw-Rate-Message":                                 "Rate limit exceeded: free-models-per-min.",
				},
			}
		}
		return &LLMResponse{Content: "should-not-run"}, nil
	})
	if resp != nil || err == nil {
		t.Fatalf("want exhausted 429, got resp=%v err=%v", resp, err)
	}
	var exhausted *RateLimitExhausted
	if !errors.As(err, &exhausted) {
		t.Fatalf("want RateLimitExhausted, got %T %v", err, err)
	}
	if calls["other:free"] != 0 {
		t.Fatalf("platform 429 tried the next model %d times", calls["other:free"])
	}
	if calls["nvidia/nemotron:free"] != rateLimitMaxHolds+1 {
		t.Fatalf("tries %d, want %d", calls["nvidia/nemotron:free"], rateLimitMaxHolds+1)
	}
	if len(slept) != rateLimitMaxHolds {
		t.Fatalf("sleeps %d, want %d", len(slept), rateLimitMaxHolds)
	}
	for _, d := range slept {
		if d != 40*time.Second {
			t.Fatalf("sleep %s, want 40s (10s retry-after + 30s cushion)", d)
		}
	}
}

func TestExecuteCandidatePlain429StillFallsThrough(t *testing.T) {
	fc := NewFallbackChain(NewCooldownTracker(), nil)
	fc.gate = NewRateLimitGate()
	fc.randN = func(int) int { return 0 }
	var slept []time.Duration
	fc.sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	calls := 0
	_, err := fc.ExecuteCandidate(context.Background(), []FallbackCandidate{
		{Provider: "openrouter", Model: "a"},
		{Provider: "openrouter", Model: "b"},
	}, func(context.Context, FallbackCandidate) (*LLMResponse, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("http error: status 429 - rate limit exceeded")
		}
		return &LLMResponse{Content: "ok"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls %d, want 2", calls)
	}
	if len(slept) != 0 {
		t.Fatalf("plain 429 string slept %v", slept)
	}
}

func TestExecuteCandidateRetriesThenSucceeds(t *testing.T) {
	fc := NewFallbackChain(NewCooldownTracker(), nil)
	fc.gate = NewRateLimitGate()
	fc.randN = func(int) int { return 4 }
	now := time.Unix(1_700_000_000, 0)
	fc.now = func() time.Time { return now }
	fc.sleep = func(ctx context.Context, d time.Duration) error {
		now = now.Add(d)
		return nil
	}
	calls := 0
	resp, err := fc.ExecuteCandidate(context.Background(), []FallbackCandidate{
		{Provider: "openrouter", Model: "a:free"},
	}, func(context.Context, FallbackCandidate) (*LLMResponse, error) {
		calls++
		if calls == 1 {
			return nil, &common.APIError{
				StatusCode: 429,
				Headers: map[string]string{
					textproto.CanonicalMIMEHeaderKey("Retry-After"): "5",
				},
			}
		}
		return &LLMResponse{Content: "ok"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil || resp.Response == nil || resp.Response.Content != "ok" {
		t.Fatalf("resp %+v", resp)
	}
	if calls != 2 {
		t.Fatalf("calls %d, want 2", calls)
	}
}
