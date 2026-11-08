package openai

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/tidwall/gjson"
)

func TestResponsesWebsocketErrorMergesSynthesizedHeaders(t *testing.T) {
	for _, addon := range []http.Header{nil, {"Retry-After": {"900"}, "X-Upstream": {"retained"}}} {
		msg := &interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Addon: addon, Headers: http.Header{
			"Retry-After": {"60"}, "Anthropic-Ratelimit-Unified-Reset": {"1793974200"},
			"Set-Cookie": {"private=value"}, "Connection": {"X-Internal"}, "X-Internal": {"private"},
			"Content-Length": {"99"}, "Transfer-Encoding": {"chunked"},
			"Access-Control-Allow-Origin": {"*"}, "X-Cpa-Trace-Id": {"upstream"}, "X-Litellm-Test": {"gateway"},
			"X-Upstream": {},
		}}
		payload, err := buildResponsesWebsocketErrorPayload(msg)
		if err != nil {
			t.Fatal(err)
		}
		headers := gjson.GetBytes(payload, "headers")
		if headers.Get("Retry-After").String() != "60" || headers.Get("Anthropic-Ratelimit-Unified-Reset").String() != "1793974200" || headers.Get("X-Upstream").String() != addon.Get("X-Upstream") {
			t.Fatalf("payload=%s", payload)
		}
		for _, key := range []string{"Set-Cookie", "Connection", "X-Internal", "Content-Length", "Transfer-Encoding", "Access-Control-Allow-Origin", "X-Cpa-Trace-Id", "X-Litellm-Test"} {
			if headers.Get(key).Exists() {
				t.Fatalf("unfiltered %s: %s", key, payload)
			}
		}
		if addon != nil && addon.Get("Retry-After") != "900" {
			t.Fatal("mutated addon headers")
		}
	}
}
