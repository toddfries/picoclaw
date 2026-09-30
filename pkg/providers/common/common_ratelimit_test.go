package common

import (
	"net/http"
	"net/textproto"
	"strings"
	"testing"
)

func TestHandleErrorResponseKeepsRateLimitHeaders(t *testing.T) {
	body := `{"error":{"message":"Rate limit exceeded: free-models-per-min. ","code":429,"metadata":{"headers":{"X-RateLimit-Limit":"20","X-RateLimit-Remaining":"0","X-RateLimit-Reset":"1700000060000"}}}}`
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     make(http.Header),
		Body:       ioNopCloser(strings.NewReader(body)),
	}
	resp.Header.Set("Retry-After", "12")
	resp.Header.Set("X-RateLimit-Limit", "20")

	err := HandleErrorResponse(resp, "https://openrouter.ai/api/v1")
	api, ok := err.(*APIError)
	if !ok {
		t.Fatalf("got %T", err)
	}
	if api.StatusCode != 429 {
		t.Fatalf("status %d", api.StatusCode)
	}
	if !strings.Contains(api.Error(), "Status: 429") {
		t.Fatalf("error text %q", api.Error())
	}
	if got := api.Headers[textproto.CanonicalMIMEHeaderKey("Retry-After")]; got != "12" {
		t.Fatalf("retry-after %q", got)
	}
	if got := api.Headers[textproto.CanonicalMIMEHeaderKey("X-RateLimit-Remaining")]; got != "0" {
		t.Fatalf("remaining %q (body copy should fill headers the HTTP response omitted)", got)
	}
	if got := api.Headers[textproto.CanonicalMIMEHeaderKey("X-RateLimit-Limit")]; got != "20" {
		t.Fatalf("limit %q, HTTP value should win", got)
	}
	if !strings.Contains(api.Headers["X-Picoclaw-Rate-Message"], "free-models-per-min") {
		t.Fatalf("message %q", api.Headers["X-Picoclaw-Rate-Message"])
	}
}

type nopClose struct{ *strings.Reader }

func (nopClose) Close() error { return nil }

func ioNopCloser(r *strings.Reader) nopClose { return nopClose{r} }
