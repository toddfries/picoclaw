package common

import (
	"encoding/json"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
)

// RateLimitHeaders copies Retry-After, every X-RateLimit-* header, and
// the same fields when OpenRouter duplicates them under
// error.metadata.headers. HTTP header values win over the body copy.
func RateLimitHeaders(h http.Header, body []byte) map[string]string {
	out := map[string]string{}
	if h != nil {
		if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
			out[textproto.CanonicalMIMEHeaderKey("Retry-After")] = v
		}
		for k, vals := range h {
			if len(vals) == 0 || strings.TrimSpace(vals[0]) == "" {
				continue
			}
			lk := strings.ToLower(k)
			if strings.HasPrefix(lk, "x-ratelimit-") || strings.HasPrefix(lk, "ratelimit-") {
				out[textproto.CanonicalMIMEHeaderKey(k)] = strings.TrimSpace(vals[0])
			}
		}
	}
	mergeBodyRateLimitHeaders(body, out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeBodyRateLimitHeaders(body []byte, dst map[string]string) {
	if len(body) == 0 {
		return
	}
	var parsed struct {
		Error struct {
			Message  string `json:"message"`
			Metadata struct {
				Headers map[string]any `json:"headers"`
			} `json:"metadata"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return
	}
	if msg := strings.TrimSpace(parsed.Error.Message); msg != "" {
		if _, ok := dst["X-Picoclaw-Rate-Message"]; !ok {
			dst["X-Picoclaw-Rate-Message"] = msg
		}
	}
	for k, raw := range parsed.Error.Metadata.Headers {
		v := stringifyHeader(raw)
		if v == "" {
			continue
		}
		ck := textproto.CanonicalMIMEHeaderKey(k)
		if _, exists := dst[ck]; exists {
			continue
		}
		dst[ck] = v
	}
}

func stringifyHeader(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		return ""
	}
}
