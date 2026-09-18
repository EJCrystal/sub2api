//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func anthropicAccountTestService(account *Account, responses ...*http.Response) (*AccountTestService, *httpUpstreamRecorder) {
	repo := &openAIAccountTestRepo{
		mockAccountRepoForGemini: mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}
	upstream := &httpUpstreamRecorder{responses: responses}
	return &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
		cfg:          rawChatCompletionsTestConfig(),
	}, upstream
}

func anthropicStreamTestResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(`data: {"type":"content_block_delta","delta":{"text":"ok"}}

data: {"type":"message_stop"}

`)),
	}
}

// The Claude-Code-style payload used by the generic Claude probe hard-coded
// "hi", so the admin test dialog's prompt input was silently ignored. It now
// carries the prompt, falling back to "hi" when empty.
func TestAccountTestService_ClaudePayloadUsesPromptAndFallsBackToHi(t *testing.T) {
	account := &Account{
		ID:          321,
		Name:        "claude-prompt-probe",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "«redacted:sk-…»",
			"base_url": "http://claude.example",
		},
	}

	svc, upstream := anthropicAccountTestService(account, anthropicStreamTestResponse())
	c, recorder := newTestContext()

	err := svc.TestAccountConnection(c, account.ID, "claude-sonnet-4", "probe-prompt", AccountTestModeDefault)

	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "http://claude.example/v1/messages?beta=true", upstream.requests[0].URL.String())
	require.Equal(t, "probe-prompt", gjson.GetBytes(upstream.bodies[0], "messages.0.content.0.text").String())
	require.True(t, gjson.GetBytes(upstream.bodies[0], "stream").Bool())
	require.Contains(t, recorder.Body.String(), `"type":"test_complete"`)

	svcDefault, upstreamDefault := anthropicAccountTestService(account, anthropicStreamTestResponse())
	cDefault, _ := newTestContext()

	err = svcDefault.TestAccountConnection(cDefault, account.ID, "claude-sonnet-4", "", AccountTestModeDefault)

	require.NoError(t, err)
	require.Len(t, upstreamDefault.requests, 1)
	require.Equal(t, "hi", gjson.GetBytes(upstreamDefault.bodies[0], "messages.0.content.0.text").String())
}
