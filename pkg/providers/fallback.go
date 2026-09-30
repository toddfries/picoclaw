package providers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// FallbackChain orchestrates model fallback across multiple candidates.
type FallbackChain struct {
	cooldown *CooldownTracker
	rl       *RateLimiterRegistry
	// now, sleep, and randN are injectable so a 429 wait can be tested
	// without sleeping. Nil uses the real clock.
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
	randN func(int) int
	gate  *RateLimitGate
}

// FallbackCandidate represents one model/provider to try.
type FallbackCandidate struct {
	Provider    string
	Model       string
	DisplayName string // optional configured alias/raw model label for persistence/UI
	RPM         int    // requests per minute; 0 means unrestricted
	IdentityKey string // optional stable config identity for cooldown/rate limiting
	ConfigIndex int    // optional 1-based model_list index selected during resolution
	ConfigKey   string // optional hashed model_list identity, stable across reordering
}

// StableKey returns the candidate's config-level identity when available,
// otherwise it falls back to the runtime provider/model key.
func (c FallbackCandidate) StableKey() string {
	if configKey := strings.TrimSpace(c.ConfigKey); configKey != "" {
		if identityKey := strings.TrimSpace(c.IdentityKey); identityKey != "" {
			return identityKey + "|" + configKey
		}
		return configKey
	}
	if key := strings.TrimSpace(c.IdentityKey); key != "" {
		return key
	}
	return ModelKey(c.Provider, c.Model)
}

// FallbackResult contains the successful response and metadata about all attempts.
type FallbackResult struct {
	Response    *LLMResponse
	Provider    string
	Model       string
	IdentityKey string
	Attempts    []FallbackAttempt
}

// FallbackAttempt records one attempt in the fallback chain.
type FallbackAttempt struct {
	Provider string
	Model    string
	Error    error
	Reason   FailoverReason
	Duration time.Duration
	Skipped  bool // true if skipped due to cooldown
}

// NewFallbackChain creates a new fallback chain with the given cooldown tracker
// and rate limiter registry.
func NewFallbackChain(cooldown *CooldownTracker, rl *RateLimiterRegistry) *FallbackChain {
	return &FallbackChain{
		cooldown: cooldown,
		rl:       rl,
		now:      time.Now,
		sleep:    sleepRateLimit,
		randN:    defaultRandN,
		gate:     processRateLimitGate,
	}
}

// Acquire enforces the local rate limit for a candidate identity. It blocks
// until a token is available or ctx is canceled, and is a no-op when no limiter
// is registered for the key (RPM == 0).
//
// ExecuteCandidate already acquires a token per candidate for multi-candidate
// configs. Callers use Acquire for the single-candidate path, where the fallback
// chain is not otherwise involved, so a configured RPM limit is honored even
// without fallbacks and while streaming.
func (fc *FallbackChain) Acquire(ctx context.Context, key string) error {
	if fc == nil || fc.rl == nil {
		return nil
	}
	return fc.rl.Wait(ctx, key)
}

// ResolveCandidates parses model config into a deduplicated candidate list.
func ResolveCandidates(cfg ModelConfig, defaultProvider string) []FallbackCandidate {
	return ResolveCandidatesWithLookup(cfg, defaultProvider, nil)
}

func ResolveCandidatesWithLookup(
	cfg ModelConfig,
	defaultProvider string,
	lookup func(raw string) (resolved string, ok bool),
) []FallbackCandidate {
	seen := make(map[string]bool)
	var candidates []FallbackCandidate

	addCandidate := func(raw string) {
		candidateRaw := strings.TrimSpace(raw)
		if lookup != nil {
			if resolved, ok := lookup(candidateRaw); ok {
				candidateRaw = resolved
			}
		}

		ref := ParseModelRef(candidateRaw, defaultProvider)
		if ref == nil {
			return
		}
		key := ModelKey(ref.Provider, ref.Model)
		if seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, FallbackCandidate{
			Provider:    ref.Provider,
			Model:       ref.Model,
			DisplayName: candidateRaw,
		})
	}

	// Primary first.
	addCandidate(cfg.Primary)

	// Then fallbacks.
	for _, fb := range cfg.Fallbacks {
		addCandidate(fb)
	}

	return candidates
}

// Execute runs the fallback chain for text/chat requests.
// It tries each candidate in order, respecting cooldowns and error classification.
//
// Behavior:
//   - Candidates in cooldown are skipped (logged as skipped attempt).
//   - context.Canceled aborts immediately (user abort, no fallback).
//   - Non-retriable errors (format) abort immediately.
//   - Retriable errors trigger fallback to next candidate.
//   - Success marks provider as good (resets cooldown).
//   - If all fail, returns aggregate error with all attempts.
func (fc *FallbackChain) Execute(
	ctx context.Context,
	candidates []FallbackCandidate,
	run func(ctx context.Context, provider, model string) (*LLMResponse, error),
) (*FallbackResult, error) {
	return fc.ExecuteCandidate(
		ctx,
		candidates,
		func(ctx context.Context, candidate FallbackCandidate) (*LLMResponse, error) {
			return run(ctx, candidate.Provider, candidate.Model)
		},
	)
}

// ExecuteCandidate runs the fallback chain and passes the complete candidate
// to the caller so model-list identity metadata remains available.
func (fc *FallbackChain) ExecuteCandidate(
	ctx context.Context,
	candidates []FallbackCandidate,
	run func(ctx context.Context, candidate FallbackCandidate) (*LLMResponse, error),
) (*FallbackResult, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("fallback: no candidates configured")
	}

	result := &FallbackResult{
		Attempts: make([]FallbackAttempt, 0, len(candidates)),
	}

	for i, candidate := range candidates {
		// Check context before each attempt.
		if ctx.Err() == context.Canceled {
			return nil, context.Canceled
		}

		// Check cooldown per stable candidate identity, not just provider/model.
		// This allows aliases and multi-key configs to fail over independently.
		cooldownKey := candidate.StableKey()
		if !fc.cooldown.IsAvailable(cooldownKey) {
			remaining := fc.cooldown.CooldownRemaining(cooldownKey)
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Skipped:  true,
				Reason:   FailoverRateLimit,
				Error: fmt.Errorf(
					"%s in cooldown (%s remaining)",
					cooldownKey,
					remaining.Round(time.Second),
				),
			})
			continue
		}

		// Enforce per-candidate rate limit before calling the provider.
		// If this candidate is locally saturated, try other candidates first.
		if fc.rl != nil {
			if !fc.rl.TryAcquire(cooldownKey) {
				if i < len(candidates)-1 {
					result.Attempts = append(result.Attempts, FallbackAttempt{
						Provider: candidate.Provider,
						Model:    candidate.Model,
						Skipped:  true,
						Reason:   FailoverRateLimit,
						Error:    fmt.Errorf("%s waiting for local rate limit token", cooldownKey),
					})
					continue
				}
				if waitErr := fc.rl.Wait(ctx, cooldownKey); waitErr != nil {
					result.Attempts = append(result.Attempts, FallbackAttempt{
						Provider: candidate.Provider,
						Model:    candidate.Model,
						Skipped:  true,
						Reason:   FailoverRateLimit,
						Error:    waitErr,
					})
					return nil, waitErr
				}
			}
		}

		// A 429 waits for the advertised reset plus a cushion and retries
		// this candidate. Walking the next free model on the same account
		// would just spend more of the shared quota.
		start := fc.clockNow()
		resp, err := fc.callRespectingRateLimit(ctx, candidate, run)
		elapsed := fc.clockNow().Sub(start)

		if err == nil {
			// Success.
			fc.cooldown.MarkSuccess(cooldownKey)
			result.Response = resp
			result.Provider = candidate.Provider
			result.Model = candidate.Model
			result.IdentityKey = candidate.StableKey()
			return result, nil
		}

		// Context cancellation: abort immediately, no fallback.
		if ctx.Err() == context.Canceled {
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Error:    err,
				Duration: elapsed,
			})
			return nil, context.Canceled
		}

		// Platform rate limit already waited and retried this candidate.
		// Do not mark a long cooldown and do not try the next model:
		// free-model caps are per account, so the next model just 429s too.
		var exhausted *RateLimitExhausted
		if errors.As(err, &exhausted) {
			reason := FailoverRateLimit
			if failErr := ClassifyError(err, candidate.Provider, candidate.Model); failErr != nil {
				reason = failErr.Reason
			}
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Error:    err,
				Reason:   reason,
				Duration: elapsed,
			})
			return nil, err
		}

		// Classify the error.
		failErr := ClassifyError(err, candidate.Provider, candidate.Model)

		if failErr == nil {
			// Unclassifiable error: do not fallback, return immediately.
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Error:    err,
				Duration: elapsed,
			})
			return nil, fmt.Errorf("fallback: unclassified error from %s/%s: %w",
				candidate.Provider, candidate.Model, err)
		}

		// Non-retriable error: abort immediately.
		if !failErr.IsRetriable() {
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Error:    failErr,
				Reason:   failErr.Reason,
				Duration: elapsed,
			})
			return nil, failErr
		}

		// Retriable error: mark failure and continue to next candidate.
		fc.cooldown.MarkFailure(cooldownKey, failErr.Reason)
		result.Attempts = append(result.Attempts, FallbackAttempt{
			Provider: candidate.Provider,
			Model:    candidate.Model,
			Error:    failErr,
			Reason:   failErr.Reason,
			Duration: elapsed,
		})

		// If this was the last candidate, return aggregate error.
		if i == len(candidates)-1 {
			return nil, &FallbackExhaustedError{Attempts: result.Attempts}
		}
	}

	// All candidates were skipped (all in cooldown).
	return nil, &FallbackExhaustedError{Attempts: result.Attempts}
}

func (fc *FallbackChain) clockNow() time.Time {
	if fc != nil && fc.now != nil {
		return fc.now()
	}
	return time.Now()
}

func (fc *FallbackChain) sleepFn() func(context.Context, time.Duration) error {
	if fc != nil && fc.sleep != nil {
		return fc.sleep
	}
	return sleepRateLimit
}

func (fc *FallbackChain) randFn() func(int) int {
	if fc != nil && fc.randN != nil {
		return fc.randN
	}
	return defaultRandN
}

func (fc *FallbackChain) gateFn() *RateLimitGate {
	if fc != nil && fc.gate != nil {
		return fc.gate
	}
	return processRateLimitGate
}

// callRespectingRateLimit retries a candidate when the provider returns
// HTTP 429. The wait is the header reset (or 60s) plus 30s and 0-20s
// of jitter. Errors that are not an HTTP 429 keep the old fallback path.
func (fc *FallbackChain) callRespectingRateLimit(
	ctx context.Context,
	candidate FallbackCandidate,
	run func(ctx context.Context, candidate FallbackCandidate) (*LLMResponse, error),
) (*LLMResponse, error) {
	holds := 0
	for {
		if err := fc.gateFn().Wait(ctx, fc.clockNow, fc.sleepFn()); err != nil {
			return nil, err
		}
		resp, err := run(ctx, candidate)
		if err == nil || ctx.Err() == context.Canceled {
			return resp, err
		}
		snap, ok := rateLimitSnapshot(err)
		if !ok {
			return nil, err
		}
		now := fc.clockNow()
		delay := snap.Delay(now, fc.randFn())
		fc.gateFn().Push(now.Add(delay), snap)
		holds++
		log.Printf(
			"picoclaw: HTTP 429 from %s/%s, waiting %s before retry (%d/%d) limit=%q remaining=%q reset=%q retry_after=%q",
			candidate.Provider, candidate.Model, delay.Round(time.Second),
			holds, snap.maxHolds(), snap.Limit, snap.Remaining, snap.Reset, snap.RetryAfter,
		)
		if holds > snap.maxHolds() {
			if snap.Platform {
				return nil, &RateLimitExhausted{Err: err, Snapshot: snap}
			}
			return nil, err
		}
		if sleepErr := fc.sleepFn()(ctx, delay); sleepErr != nil {
			return nil, sleepErr
		}
	}
}

// ExecuteImage runs the fallback chain for image/vision requests.
// Simpler than Execute: no cooldown checks (image endpoints have different rate limits).
// Image dimension/size errors abort immediately (non-retriable).
func (fc *FallbackChain) ExecuteImage(
	ctx context.Context,
	candidates []FallbackCandidate,
	run func(ctx context.Context, provider, model string) (*LLMResponse, error),
) (*FallbackResult, error) {
	return fc.ExecuteImageCandidate(
		ctx,
		candidates,
		func(ctx context.Context, candidate FallbackCandidate) (*LLMResponse, error) {
			return run(ctx, candidate.Provider, candidate.Model)
		},
	)
}

// ExecuteImageCandidate preserves model-list identity metadata for each image
// fallback attempt.
func (fc *FallbackChain) ExecuteImageCandidate(
	ctx context.Context,
	candidates []FallbackCandidate,
	run func(ctx context.Context, candidate FallbackCandidate) (*LLMResponse, error),
) (*FallbackResult, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("image fallback: no candidates configured")
	}

	result := &FallbackResult{
		Attempts: make([]FallbackAttempt, 0, len(candidates)),
	}

	for i, candidate := range candidates {
		if ctx.Err() == context.Canceled {
			return nil, context.Canceled
		}

		// Enforce per-candidate rate limit before calling the provider.
		// If this candidate is locally saturated, try other candidates first.
		imageKey := candidate.StableKey()
		if fc.rl != nil {
			if !fc.rl.TryAcquire(imageKey) {
				if i < len(candidates)-1 {
					result.Attempts = append(result.Attempts, FallbackAttempt{
						Provider: candidate.Provider,
						Model:    candidate.Model,
						Skipped:  true,
						Reason:   FailoverRateLimit,
						Error:    fmt.Errorf("%s waiting for local rate limit token", imageKey),
					})
					continue
				}
				if waitErr := fc.rl.Wait(ctx, imageKey); waitErr != nil {
					result.Attempts = append(result.Attempts, FallbackAttempt{
						Provider: candidate.Provider,
						Model:    candidate.Model,
						Skipped:  true,
						Reason:   FailoverRateLimit,
						Error:    waitErr,
					})
					return nil, waitErr
				}
			}
		}

		start := time.Now()
		resp, err := run(ctx, candidate)
		elapsed := time.Since(start)

		if err == nil {
			result.Response = resp
			result.Provider = candidate.Provider
			result.Model = candidate.Model
			result.IdentityKey = candidate.StableKey()
			return result, nil
		}

		if ctx.Err() == context.Canceled {
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Error:    err,
				Duration: elapsed,
			})
			return nil, context.Canceled
		}

		// Image dimension/size errors are non-retriable.
		errMsg := strings.ToLower(err.Error())
		if IsImageDimensionError(errMsg) || IsImageSizeError(errMsg) {
			result.Attempts = append(result.Attempts, FallbackAttempt{
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Error:    err,
				Reason:   FailoverFormat,
				Duration: elapsed,
			})
			return nil, &FailoverError{
				Reason:   FailoverFormat,
				Provider: candidate.Provider,
				Model:    candidate.Model,
				Wrapped:  err,
			}
		}

		// Any other error: record and try next.
		result.Attempts = append(result.Attempts, FallbackAttempt{
			Provider: candidate.Provider,
			Model:    candidate.Model,
			Error:    err,
			Duration: elapsed,
		})

		if i == len(candidates)-1 {
			return nil, &FallbackExhaustedError{Attempts: result.Attempts}
		}
	}

	return nil, &FallbackExhaustedError{Attempts: result.Attempts}
}

// FallbackExhaustedError indicates all fallback candidates were tried and failed.
type FallbackExhaustedError struct {
	Attempts []FallbackAttempt
}

func (e *FallbackExhaustedError) Error() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("fallback: all %d candidates failed:", len(e.Attempts)))
	for i, a := range e.Attempts {
		if a.Skipped {
			sb.WriteString(fmt.Sprintf("\n  [%d] %s/%s: skipped (cooldown)", i+1, a.Provider, a.Model))
		} else {
			sb.WriteString(fmt.Sprintf("\n  [%d] %s/%s: %v (reason=%s, %s)",
				i+1, a.Provider, a.Model, a.Error, a.Reason, a.Duration.Round(time.Millisecond)))
		}
	}
	return sb.String()
}
