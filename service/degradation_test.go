package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Upstream fixtures honor stream:true without invoking production parsing.
func writeDegradationTestStream(w http.ResponseWriter, body, format string) {
	w.Header().Set("Content-Type", "text/event-stream")
	write := func(value any) {
		encoded, _ := common.Marshal(value)
		_, _ = io.WriteString(w, "data: "+string(encoded)+"\n\n")
	}
	if format == "chat" {
		_, _ = io.WriteString(w, "data: "+strings.ReplaceAll(body, `"message":`, `"delta":`)+"\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		return
	}
	var snapshot struct {
		Output []struct {
			Type    string           `json:"type"`
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"output"`
		Content []map[string]any `json:"content"`
	}
	if common.UnmarshalJsonStr(body, &snapshot) != nil {
		_, _ = io.WriteString(w, "data: invalid\n\n")
		return
	}
	if format == "responses" {
		for _, output := range snapshot.Output {
			for _, part := range output.Content {
				kind := "response.reasoning_text.delta"
				if output.Type == "message" && (output.Role == "" || output.Role == "assistant") {
					kind = "response.output_text.delta"
				}
				write(map[string]any{"type": kind, "delta": part["text"]})
			}
		}
		write(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed"}})
	} else {
		for _, part := range snapshot.Content {
			kind := "thinking_delta"
			if part["type"] == "text" {
				kind = "text_delta"
			}
			write(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": kind, "text": part["text"]}})
		}
		write(map[string]any{"type": "message_stop"})
	}
}

type degradationReaderFunc func([]byte) (int, error)

func (read degradationReaderFunc) Read(buffer []byte) (int, error) { return read(buffer) }

func TestDegradationStreamingTTFT(t *testing.T) {
	for _, scenario := range []struct {
		format string
		chunks []string
	}{
		{"chat", []string{
			": keepalive\r\n\r\ndata: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\",\"reasoning_content\":\"starting number\"}}]}\r\n\r\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"I can\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{\"content\":\"'t\"}}]}\n\n",
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		}},
		{"responses", []string{
			"event: response.reasoning_text.delta\ndata: {\"delta\":\"starting number\"}\n\n",
			"event: response.output_text.delta\ndata: {\"delta\":\"I can\"}\n\n",
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"'t\"}\n\n",
			"event: response.completed\ndata: {\"response\":{\"status\":\"completed\"}}\n\n",
		}},
		{"anthropic", []string{
			"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"starting number\"}}\n\n",
			"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"I can\"}}\n\n",
			"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"'t\"}}\n\n",
			"data: {\"type\":\"message_stop\"}\n\n",
		}},
	} {
		t.Run(scenario.format, func(t *testing.T) {
			var clock time.Duration
			index := 0
			reader := degradationReaderFunc(func(buffer []byte) (int, error) {
				if index == len(scenario.chunks) {
					return 0, io.EOF
				}
				clock = []time.Duration{10, 60, 80, 200}[index] * time.Millisecond
				n := copy(buffer, scenario.chunks[index])
				scenario.chunks[index] = scenario.chunks[index][n:]
				if scenario.chunks[index] == "" {
					index++
				}
				return n, nil
			})
			text, ttft, code := readDegradationStream(context.Background(), reader, scenario.format, func() time.Duration { return clock })
			require.Empty(t, code)
			assert.Equal(t, "I can't", text)
			require.NotNil(t, ttft)
			assert.EqualValues(t, 60, *ttft, "headers, keepalives and reasoning must not start TTFT")
			assert.Equal(t, 200*time.Millisecond, clock, "completion must not replace TTFT")
		})
	}
	for _, scenario := range []struct {
		name, format, body, text, code string
		measured                       bool
	}{
		{"truncated chat", "chat", "data: {\"choices\":[{\"delta\":{\"content\":\"can't\"}}]}\n\n", "", "invalid_response", true},
		{"stream error after text", "chat", "data: {\"choices\":[{\"delta\":{\"content\":\"can't\"}}]}\n\ndata: {\"error\":{\"message\":\"secret-canary\"}}\n\n", "", "stream_error", true},
		{"only role", "chat", "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: [DONE]\n\n", "", "invalid_response", false},
		{"empty answer", "chat", "data: {\"choices\":[{\"delta\":{\"content\":\"\"}}]}\n\ndata: [DONE]\n\n", "", "", false},
		{"multiline refusal", "chat", "data: {\"choices\":[\n" + "data: {\"delta\":{\"refusal\":\"can't\"}}]}\n\ndata: [DONE]\n\n", "can't", "", true},
		{"Responses refusal", "responses", "data: {\"type\":\"response.refusal.delta\",\"delta\":\"can't\"}\n\ndata: {\"type\":\"response.completed\"}\n\n", "can't", "", true},
		{"Responses incomplete", "responses", "data: {\"type\":\"response.incomplete\"}\n\n", "", "stream_error", false},
		{"Anthropic error", "anthropic", "event: error\ndata: {\"error\":{\"message\":\"secret-canary\"}}\n\n", "", "stream_error", false},
		{"completion-only has no TTFT", "responses", "data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"can't\"}]}]}}\n\n", "can't\n", "", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			text, ttft, code := readDegradationStream(context.Background(), strings.NewReader(scenario.body), scenario.format, func() time.Duration { return 0 })
			assert.Equal(t, scenario.text, text)
			assert.Equal(t, scenario.code, code)
			if scenario.measured {
				require.NotNil(t, ttft)
				assert.Zero(t, *ttft, "zero ms is a valid measured value")
			} else {
				assert.Nil(t, ttft)
			}
		})
	}
	t.Run("read failure after first delta remains inconclusive", func(t *testing.T) {
		chunk := "data: {\"choices\":[{\"delta\":{\"content\":\"can't\"}}]}\n\n"
		reader := degradationReaderFunc(func(buffer []byte) (int, error) {
			if chunk == "" {
				return 0, io.ErrUnexpectedEOF
			}
			n := copy(buffer, chunk)
			chunk = chunk[n:]
			return n, nil
		})
		text, ttft, code := readDegradationStream(context.Background(), reader, "chat", func() time.Duration { return time.Millisecond })
		assert.Empty(t, text)
		assert.Equal(t, "read_error", code)
		require.NotNil(t, ttft)
		assert.EqualValues(t, 1, *ttft)
	})
	t.Run("non-streaming fallback remains inconclusive", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"can't"}}]}`)
		}))
		defer server.Close()
		result := ProbeDegradation(context.Background(), DegradationTarget{Endpoint: server.URL, Model: "model", APIKey: "key", AllowHTTP: true})
		assert.Equal(t, "streaming_not_supported", result.ErrorCode)
		assert.Nil(t, result.TTFTMs)
		assert.Nil(t, result.IsDegraded)
	})
}

func TestDegradationRule(t *testing.T) {
	for _, scenario := range []struct {
		text, marker string
		degraded     bool
	}{
		{"I can’t disclose that.", "can't", false},
		{"I CAN'T disclose that.", "can't", false},
		{"Please give the original\nnumber.", "original number", false},
		{"What is the STARTING NUMBER?", "starting number", false},
		{"My assistant guidelines prevent this.", "assistant guidelines", false},
		{"16", "", true}, {"I cannot disclose it", "", true}, {"", "", true},
	} {
		t.Run(scenario.text, func(t *testing.T) {
			degraded, marker := ClassifyDegradationText(scenario.text)
			assert.Equal(t, scenario.degraded, degraded)
			assert.Equal(t, scenario.marker, marker)
		})
	}
}

func TestDegradationProbeProtocolsAndSafety(t *testing.T) {
	for _, scenario := range []struct {
		name, format, model, body, state, code string
		status                                 int
	}{
		{"chat refusal", "chat", "model-a", `{"choices":[{"message":{"role":"assistant","content":"I can’t reveal private-response-canary"}}]}`, "normal", "", 200},
		{"chat numeric", "chat", "gpt-5", `{"choices":[{"message":{"content":"16"}}]}`, "abnormal", "", 200},
		{"chat blocks", "chat", "model-a", `{"choices":[{"message":{"content":[{"type":"text","text":"original number"}]}}]}`, "normal", "", 200},
		{"chat refusal field", "chat", "model-a", `{"choices":[{"message":{"content":null,"refusal":"assistant guidelines"}}]}`, "normal", "", 200},
		{"hidden reasoning ignored", "chat", "model-a", `{"choices":[{"message":{"content":"42","reasoning_content":"starting number"}}],"metadata":"assistant guidelines"}`, "abnormal", "", 200},
		{"responses", "responses", "model-a", `{"output":[{"type":"reasoning","content":[{"type":"output_text","text":"private-response-canary"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"starting number"}]}]}`, "normal", "", 200},
		{"native claude", "anthropic", "claude-model", `{"content":[{"type":"thinking","thinking":"private-response-canary"},{"type":"text","text":"assistant guidelines"}]}`, "normal", "", 200},
		{"empty answer", "chat", "model-a", `{"choices":[{"message":{"content":""}}]}`, "abnormal", "", 200},
		{"no answer", "chat", "model-a", `{"choices":[]}`, "error", "invalid_response", 200},
		{"invalid json", "chat", "model-a", `can’t`, "error", "invalid_response", 200},
		{"business error", "chat", "model-a", `{"error":{"message":"can’t"}}`, "error", "stream_error", 200},
		{"http error", "chat", "model-a", `{"choices":[{"message":{"content":"can’t private-response-canary"}}]}`, "error", "http_error", 503},
		{"too large", "chat", "model-a", strings.Repeat("x", degradationResponseLimit+1), "error", "response_too_large", 200},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if !assert.NoError(t, err) {
					return
				}
				var payload map[string]any
				if !assert.NoError(t, common.Unmarshal(body, &payload)) {
					return
				}
				assert.Equal(t, scenario.model, payload["model"])
				assert.Equal(t, true, payload["stream"])
				messageKey, limitKey := "messages", "max_tokens"
				if scenario.format == "responses" {
					messageKey, limitKey = "input", "max_output_tokens"
					assert.Equal(t, false, payload["store"])
				}
				if scenario.model == "gpt-5" {
					limitKey = "max_completion_tokens"
					assert.NotContains(t, payload, "max_tokens")
				}
				assert.EqualValues(t, 512, payload[limitKey])
				assert.Equal(t, []any{map[string]any{"role": "user", "content": DegradationPrompt}}, payload[messageKey])
				if scenario.format == "anthropic" {
					assert.Equal(t, "private-key-canary", r.Header.Get("x-api-key"))
					assert.Equal(t, "2023-06-01", r.Header.Get("anthropic-version"))
					assert.Empty(t, r.Header.Get("Authorization"))
				} else {
					assert.Equal(t, "Bearer private-key-canary", r.Header.Get("Authorization"))
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(scenario.status)
				writeDegradationTestStream(w, scenario.body, scenario.format)
			}))
			defer server.Close()
			result := ProbeDegradation(context.Background(), DegradationTarget{Endpoint: server.URL, Model: scenario.model, APIKey: "private-key-canary", Format: scenario.format, AllowHTTP: true})
			assert.Equal(t, scenario.state, result.State)
			assert.Equal(t, scenario.code, result.ErrorCode)
			assert.Equal(t, scenario.status, result.HTTPStatus)
			if scenario.state == "error" {
				assert.Nil(t, result.IsDegraded)
				assert.False(t, result.IsOnline)
			} else {
				require.NotNil(t, result.IsDegraded)
				assert.Equal(t, scenario.state == "abnormal", *result.IsDegraded)
				assert.True(t, result.IsOnline)
			}
			assert.Equal(t, DegradationRuleVersion, result.RuleVersion)
			assert.GreaterOrEqual(t, result.CompletedAt, result.StartedAt)
			encoded, err := common.Marshal(result)
			require.NoError(t, err)
			for _, secret := range []string{"private-key-canary", "private-response-canary", server.URL} {
				assert.NotContains(t, string(encoded), secret)
			}
		})
	}

	t.Run("HTTP is opt-in and redirects never forward secrets", func(t *testing.T) {
		var targetHits atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHits.Add(1) }))
		defer target.Close()
		redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		}))
		defer redirect.Close()
		request := DegradationTarget{Endpoint: redirect.URL, Model: "model", APIKey: "private-key-canary"}
		assert.Equal(t, "invalid_target", ProbeDegradation(context.Background(), request).ErrorCode)
		request.AllowHTTP = true
		result := ProbeDegradation(context.Background(), request)
		assert.Equal(t, "http_error", result.ErrorCode)
		assert.Equal(t, http.StatusTemporaryRedirect, result.HTTPStatus)
		assert.Zero(t, targetHits.Load())
		for _, endpoint := range []string{"https://user:password@example.test", "https://example.test/?key=secret", "ftp://example.test"} {
			request.Endpoint = endpoint
			assert.Equal(t, "invalid_target", ProbeDegradation(context.Background(), request).ErrorCode)
		}
	})

	t.Run("cancellation and timeout do not become degradation verdicts", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
		defer server.Close()
		target := DegradationTarget{Endpoint: server.URL, Model: "model", APIKey: "key", AllowHTTP: true, Timeout: time.Second}
		result := ProbeDegradation(context.Background(), target)
		assert.Equal(t, "timeout", result.ErrorCode)
		assert.Nil(t, result.IsDegraded)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		assert.Equal(t, "cancelled", ProbeDegradation(ctx, target).ErrorCode)
	})
}

func TestDegradationCLI(t *testing.T) {
	t.Setenv("DEGRADATION_TEST_KEY", "private-key-canary")
	t.Setenv("SQL_DSN", "invalid-database-canary")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/normal" {
			writeDegradationTestStream(w, `{"choices":[{"message":{"content":"can't private-response-canary"}}]}`, "chat")
		} else {
			writeDegradationTestStream(w, `{"choices":[{"message":{"content":"16"}}]}`, "chat")
		}
	}))
	defer server.Close()
	for _, scenario := range []struct {
		path  string
		exit  int
		state string
	}{{"/normal", 0, "normal"}, {"/abnormal", 1, "abnormal"}} {
		var out, stderr bytes.Buffer
		code := RunDegradationProbeCLI(context.Background(), []string{"--endpoint", server.URL + scenario.path, "--model", "model", "--api-key-env", "DEGRADATION_TEST_KEY", "--allow-http"}, &out, &stderr)
		assert.Equal(t, scenario.exit, code)
		var result DegradationResult
		require.NoError(t, common.Unmarshal(out.Bytes(), &result))
		assert.Equal(t, scenario.state, result.State)
		assert.NotContains(t, out.String()+stderr.String(), "private-key-canary")
		assert.NotContains(t, out.String()+stderr.String(), "private-response-canary")
		assert.Empty(t, stderr.String())
	}
	var out, stderr bytes.Buffer
	assert.Equal(t, 2, RunDegradationProbeCLI(context.Background(), []string{"--timeout=private-key-canary"}, &out, &stderr))
	assert.NotContains(t, stderr.String(), "private-key-canary")
	out.Reset()
	stderr.Reset()
	assert.Equal(t, 0, RunDegradationProbeCLI(context.Background(), []string{"--help"}, &out, &stderr))
	assert.Contains(t, out.String(), "Heuristic only")
}

func TestDegradationCLINeverBootstrapsDatabase(t *testing.T) {
	previous := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = previous })
	t.Setenv("SQL_DSN", "local")
	path := filepath.Join(t.TempDir(), "must-not-create.db")
	t.Setenv("SQLITE_PATH", path)
	var out, stderr bytes.Buffer
	assert.Equal(t, 0, RunDegradationCLI(context.Background(), []string{"--help"}, &out, &stderr))
	assert.Contains(t, out.String(), "--provider-id")
	out.Reset()
	stderr.Reset()
	assert.Equal(t, 2, RunDegradationCLI(context.Background(), []string{"--dry-run"}, &out, &stderr))
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err))
	assert.Nil(t, model.DB)
}

func TestAdvancedCustomDegradationRoutes(t *testing.T) {
	var authKind atomic.Int32 // 0=header, 1=query, 2=none
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/chat/target-model", r.URL.Path)
		switch authKind.Load() {
		case 0:
			assert.Equal(t, "key-route-secret-canary", r.Header.Get("X-Route-Key"))
		case 1:
			assert.Empty(t, r.Header.Get("X-Route-Key"))
			assert.Equal(t, "secret-canary", r.URL.Query().Get("key"))
		case 2:
			assert.Empty(t, r.Header.Get("X-Route-Key"))
			assert.Empty(t, r.URL.Query().Get("key"))
		}
		assert.Empty(t, r.Header.Get("Authorization"))
		assert.Equal(t, "model-a", r.URL.Query().Get("version"))
		var payload map[string]any
		assert.NoError(t, common.DecodeJson(r.Body, &payload))
		assert.Equal(t, "target-model", payload["model"])
		writeDegradationTestStream(w, `{"choices":[{"message":{"content":"can't share secret-response-canary"}}]}`, "chat")
	}))
	defer server.Close()
	base := server.URL
	mapping := `{"model-a":"target-model"}`
	imageModel := "gpt-image-2"
	channel := model.Channel{Type: constant.ChannelTypeAdvancedCustom, Models: "gpt-image-2,model-a", TestModel: &imageModel, Key: "secret-canary", BaseURL: &base, ModelMapping: &mapping}
	other := dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{
		{IncomingPath: "/v1/chat/completions", UpstreamPath: "/chat/{model}?version=model-a", Auth: &dto.AdvancedCustomRouteAuth{Type: dto.AdvancedCustomAuthTypeHeader, Name: "X-Route-Key", Value: "key-route-{api_key}"}},
		{IncomingPath: "/v1/images/generations", UpstreamPath: "/images", Auth: &dto.AdvancedCustomRouteAuth{Type: dto.AdvancedCustomAuthTypeQuery, Name: "api_key", Value: "{api_key}"}},
	}}}
	encoded, err := common.Marshal(other)
	require.NoError(t, err)
	channel.OtherSettings = string(encoded)
	config := DegradationTaskConfig{Timeout: time.Second, AllowHTTP: true, Format: "chat"}
	representative, chosen, reason := channelDegradationTarget(&channel, config, "")
	assert.Empty(t, reason)
	assert.Equal(t, "model-a", chosen)
	assert.NotEmpty(t, representative.Endpoint)
	target, name, skip := channelDegradationTarget(&channel, config, "model-a")
	assert.Empty(t, skip)
	assert.Equal(t, "model-a", name)
	result := ProbeDegradation(context.Background(), target)
	assert.Equal(t, "normal", result.State)
	data, err := common.Marshal(result)
	require.NoError(t, err)
	for _, secret := range []string{server.URL, "secret-canary", "secret-response-canary"} {
		assert.NotContains(t, string(data), secret)
	}
	_, _, skip = channelDegradationTarget(&channel, config, "gpt-image-2")
	assert.Equal(t, "unsupported_model_modality", skip)
	other.AdvancedCustom.Routes[0].Converter = "openai_chat_to_claude_messages"
	encoded, err = common.Marshal(other)
	require.NoError(t, err)
	channel.OtherSettings = string(encoded)
	_, _, skip = channelDegradationTarget(&channel, config, "model-a")
	assert.NotEmpty(t, skip, "a converter cannot be silently bypassed")
	other.AdvancedCustom.Routes[0].Converter = ""
	other.AdvancedCustom.Routes[0].Auth = &dto.AdvancedCustomRouteAuth{Type: dto.AdvancedCustomAuthTypeQuery, Name: "key", Value: "{api_key}"}
	encoded, err = common.Marshal(other)
	require.NoError(t, err)
	channel.OtherSettings = string(encoded)
	target, _, skip = channelDegradationTarget(&channel, config, "model-a")
	assert.Empty(t, skip)
	u, err := url.Parse(target.Endpoint)
	require.NoError(t, err)
	assert.Equal(t, "secret-canary", u.Query().Get("key"))
	assert.True(t, target.SkipDefaultAuth)
	authKind.Store(1)
	assert.Equal(t, "normal", ProbeDegradation(context.Background(), target).State)
	other.AdvancedCustom.Routes[0].Auth = &dto.AdvancedCustomRouteAuth{Type: dto.AdvancedCustomAuthTypeNone}
	channel.Key = ""
	encoded, err = common.Marshal(other)
	require.NoError(t, err)
	channel.OtherSettings = string(encoded)
	target, _, skip = channelDegradationTarget(&channel, config, "model-a")
	assert.Empty(t, skip)
	authKind.Store(2)
	assert.Equal(t, "normal", ProbeDegradation(context.Background(), target).State)
}

func TestDegradationTaskConfiguration(t *testing.T) {
	t.Setenv("DEGRADATION_CHECK_ENABLED", "false")
	config, err := DegradationConfigFromEnv()
	require.NoError(t, err)
	assert.False(t, config.Enabled)
	assert.Equal(t, 8, config.Concurrency)
	previousMaster := common.IsMasterNode
	common.IsMasterNode = true
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	assert.False(t, (channelDegradationHandler{}).Enabled())
	t.Setenv("DEGRADATION_CHECK_ENABLED", "true")
	t.Setenv("DEGRADATION_CHECK_INTERVAL_MINUTES", "15")
	t.Setenv("DEGRADATION_CHECK_TIMEOUT_SECONDS", "20")
	t.Setenv("DEGRADATION_CHECK_BATCH_SIZE", "3")
	t.Setenv("DEGRADATION_CHECK_CONCURRENCY", "4")
	t.Setenv("DEGRADATION_CHECK_FORMAT", "responses")
	config, err = DegradationConfigFromEnv()
	require.NoError(t, err)
	assert.Equal(t, 15*time.Minute, config.Interval)
	assert.Equal(t, 20*time.Second, config.Timeout)
	assert.Equal(t, 3, config.BatchSize)
	assert.Equal(t, 4, config.Concurrency)
	assert.Equal(t, "responses", config.Format)
	assert.True(t, (channelDegradationHandler{}).Enabled())
	common.IsMasterNode = false
	assert.False(t, (channelDegradationHandler{}).Enabled())
	for _, bounds := range []struct{ name, value string }{
		{"DEGRADATION_CHECK_INTERVAL_MINUTES", "0"},
		{"DEGRADATION_CHECK_TIMEOUT_SECONDS", "301"},
		{"DEGRADATION_CHECK_BATCH_SIZE", "1001"},
		{"DEGRADATION_CHECK_CONCURRENCY", "0"},
		{"DEGRADATION_CHECK_CONCURRENCY", "65"},
	} {
		t.Run(bounds.name+" bounds", func(t *testing.T) {
			t.Setenv(bounds.name, bounds.value)
			_, err := DegradationConfigFromEnv()
			require.Error(t, err)
		})
	}
	for _, name := range []string{"DEGRADATION_CHECK_ENABLED", "DEGRADATION_CHECK_INTERVAL_MINUTES", "DEGRADATION_CHECK_TIMEOUT_SECONDS", "DEGRADATION_CHECK_BATCH_SIZE", "DEGRADATION_CHECK_FORMAT", "DEGRADATION_CHECK_ALLOW_HTTP", "DEGRADATION_CHECK_CONCURRENCY"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "invalid-canary")
			_, err := DegradationConfigFromEnv()
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "invalid-canary")
		})
	}
}

func TestInterruptedDegradationObservationsRemainDurable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "interrupted.db")), &gorm.Config{})
	require.NoError(t, err)
	previous := model.DB
	model.DB = db
	t.Cleanup(func() {
		model.DB = previous
		pool, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, pool.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.Provider{}, &model.Channel{}, &model.ChannelDegradationSample{}, &model.SystemTaskLock{}))
	channel := model.Channel{Name: "interrupted", ProviderID: 1, Models: "model-a", Key: "private-key"}
	require.NoError(t, db.Create(&channel).Error)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, scenario := range []struct {
		name string
		ctx  context.Context
		err  error
	}{{"cancelled after completion", cancelled, context.Canceled}, {"lost task lease", context.Background(), model.ErrSystemTaskLockLost}} {
		t.Run(scenario.name, func(t *testing.T) {
			latency := int64(50)
			observation := DegradationResult{ID: uuid.NewString(), State: "normal", StartedAt: time.Now().UnixMilli() - 100, CompletedAt: time.Now().UnixMilli(), DurationMs: 100, TTFTMs: &latency}
			sample, err := recordDegradationObservation(scenario.ctx, &channel, "model-a", DegradationTarget{Format: "chat", Timeout: time.Second}, observation, "expired-task", "runner", false)
			assert.ErrorIs(t, err, scenario.err)
			assert.False(t, sample.SnapshotApplied)
			var persisted model.ChannelDegradationSample
			require.NoError(t, db.First(&persisted, "id = ?", observation.ID).Error)
			assert.Equal(t, "normal", persisted.State, "a completed success must not be lost or reclassified by later cancellation")
			require.NotNil(t, persisted.TTFTMs)
			assert.EqualValues(t, 50, *persisted.TTFTMs)
			var current model.Channel
			require.NoError(t, db.First(&current, channel.Id).Error)
			assert.Nil(t, current.LastCheckedAt)
			assert.Nil(t, current.IsOnline)
		})
	}
	for _, code := range []string{"cancelled", "invalid_target", "invalid_format", "invalid_proxy", "invalid_request"} {
		sample := model.ChannelDegradationSample{ID: uuid.NewString(), State: "error", ErrorCode: code, Model: "model-a", StartedAt: time.Now().UnixMilli(), CompletedAt: time.Now().UnixMilli()}
		require.NoError(t, model.RecordChannelDegradation(context.Background(), &channel, &sample, ""))
		assert.False(t, sample.SnapshotApplied, "inconclusive attempts cannot mark a channel offline")
	}
	var current model.Channel
	require.NoError(t, db.First(&current, channel.Id).Error)
	assert.Nil(t, current.IsOnline)
	assert.Nil(t, current.LastCheckedAt)
}

func TestDegradationConcurrentExecution(t *testing.T) {
	for _, scenario := range []struct {
		name, env              string
		scheduled, cancel      bool
		override, limit, total int
	}{
		{name: "CLI default", limit: 8, total: 10},
		{name: "CLI configured with timer disabled", env: "4", limit: 4, total: 10},
		{name: "scheduled configured", env: "3", scheduled: true, limit: 3, total: 5},
		{name: "CLI flag overrides environment", env: "3", override: 2, limit: 2, total: 10},
		{name: "CLI cancellation", cancel: true, limit: 8, total: 8},
		{name: "scheduled cancellation", env: "3", scheduled: true, cancel: true, limit: 3, total: 3},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "concurrent.db")), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			previousDB, previousType, previousMaster := model.DB, common.MainDatabaseType(), common.IsMasterNode
			model.DB, common.IsMasterNode = db, true
			common.SetMainDatabaseType(common.DatabaseTypeSQLite)
			t.Cleanup(func() {
				model.DB, common.IsMasterNode = previousDB, previousMaster
				common.SetMainDatabaseType(previousType)
				require.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.AutoMigrate(&model.Provider{}, &model.Model{}, &model.Channel{}, &model.ChannelDegradationSample{}, &model.SystemTask{}, &model.SystemTaskLock{}))
			t.Setenv("DEGRADATION_CHECK_ENABLED", strconv.FormatBool(scenario.scheduled))
			t.Setenv("DEGRADATION_CHECK_ALLOW_HTTP", "true")
			t.Setenv("DEGRADATION_CHECK_CONCURRENCY", scenario.env)
			if scenario.env == "" {
				require.NoError(t, os.Unsetenv("DEGRADATION_CHECK_CONCURRENCY"))
			}
			gate, started := make(chan struct{}), make(chan struct{}, 16)
			var active, peak atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				current := active.Add(1)
				defer active.Add(-1)
				for {
					old := peak.Load()
					if current <= old || peak.CompareAndSwap(old, current) {
						break
					}
				}
				started <- struct{}{}
				select {
				case <-gate:
					writeDegradationTestStream(w, `{"choices":[{"message":{"content":"can't"}}]}`, "chat")
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			provider := model.Provider{Name: "concurrent"}
			require.NoError(t, provider.Save())
			enabled := true
			for _, name := range []string{"m-a", "m-b"} {
				require.NoError(t, db.Create(&model.Model{ModelName: name, DegradationCheckEnabled: &enabled}).Error)
			}
			base := server.URL
			for range 5 {
				require.NoError(t, db.Create(&model.Channel{Type: constant.ChannelTypeOpenAI, ProviderID: provider.ID, Key: "key", Models: "m-a,m-b", BaseURL: &base}).Error)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var task *model.SystemTask
			if scenario.scheduled {
				task, err = model.CreateSystemTask(model.SystemTaskTypeChannelDegradation, degradationTaskPayload{}, nil)
				require.NoError(t, err)
				var claimed bool
				task, claimed, err = model.ClaimSystemTask(task.ID, task.Type, "runner", common.GetTimestamp()+60)
				require.NoError(t, err)
				require.True(t, claimed)
			}
			done := make(chan struct{})
			var output, stderr bytes.Buffer
			exit := 0
			go func() {
				defer close(done)
				if scenario.scheduled {
					(channelDegradationHandler{}).Run(ctx, task, "runner")
				} else {
					args := []string{"--allow-http"}
					if scenario.override > 0 {
						args = append(args, "--concurrency", strconv.Itoa(scenario.override))
					}
					exit = RunDegradationCLI(ctx, args, &output, &stderr)
				}
			}()
			defer func() { cancel(); <-done }()
			// A gate, not elapsed-time comparisons, proves that exactly the configured
			// number of requests can be in flight before any response is released.
			for range scenario.limit {
				select {
				case <-started:
				case <-done:
					t.Fatal("scan ended before filling worker pool")
				case <-ctx.Done():
					t.Fatal("worker pool did not dispatch concurrently")
				}
			}
			if scenario.cancel {
				cancel()
			} else {
				close(gate)
			}
			<-done
			assert.EqualValues(t, scenario.limit, peak.Load())
			if scenario.cancel && !scenario.scheduled {
				assert.Equal(t, 2, exit)
			} else if !scenario.scheduled {
				assert.Zero(t, exit, stderr.String())
			}
			if scenario.scheduled {
				finished, err := model.GetSystemTaskByTaskID(task.TaskID)
				require.NoError(t, err)
				if scenario.cancel {
					assert.Equal(t, model.SystemTaskStatusFailed, finished.Status)
				} else {
					assert.Equal(t, model.SystemTaskStatusSucceeded, finished.Status)
				}
			}
			var samples, latencies int64
			require.NoError(t, db.Model(&model.ChannelDegradationSample{}).Count(&samples).Error)
			assert.EqualValues(t, scenario.total, samples)
			require.NoError(t, db.Model(&model.ChannelDegradationSample{}).Where("ttft_ms IS NOT NULL").Count(&latencies).Error)
			if scenario.cancel {
				assert.Zero(t, latencies)
				var cancelled int64
				require.NoError(t, db.Model(&model.ChannelDegradationSample{}).Where("error_code = ? AND snapshot_applied = ?", "cancelled", false).Count(&cancelled).Error)
				assert.Equal(t, samples, cancelled, "every started attempt is durable without publishing an offline snapshot")
				require.NoError(t, db.Model(&model.Channel{}).Where("last_checked_at IS NOT NULL").Count(&latencies).Error)
				assert.Zero(t, latencies)
			} else {
				assert.Equal(t, samples, latencies)
			}
			if !scenario.cancel {
				var observed []model.Channel
				require.NoError(t, db.Find(&observed).Error)
				latencies = 0
				for _, ch := range observed {
					for _, cfg := range ch.ModelConfigs {
						if cfg.TTFTMs != nil {
							latencies++
						}
					}
				}
				assert.EqualValues(t, scenario.total, latencies)
				if !scenario.scheduled {
					previous := 0
					for line := range strings.SplitSeq(strings.TrimSpace(output.String()), "\n") {
						var event degradationScanEvent
						require.NoError(t, common.UnmarshalJsonStr(line, &event))
						if event.Kind == "result" {
							assert.GreaterOrEqual(t, event.ChannelID, previous)
							previous = event.ChannelID
						}
					}
				}
			}
		})
	}
}

func TestDegradationSchedulerOptInAndRoundRobin(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "scheduler.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previousDB, previousType, previousMaster := model.DB, common.MainDatabaseType(), common.IsMasterNode
	model.DB, common.IsMasterNode = db, true
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	systemTaskHandlersMu.Lock()
	previousHandlers := systemTaskHandlers
	systemTaskHandlers = make(map[string]SystemTaskHandler)
	systemTaskHandlersMu.Unlock()
	t.Cleanup(func() {
		model.DB, common.IsMasterNode = previousDB, previousMaster
		common.SetMainDatabaseType(previousType)
		systemTaskHandlersMu.Lock()
		systemTaskHandlers = previousHandlers
		systemTaskHandlersMu.Unlock()
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.Provider{}, &model.Model{}, &model.Channel{}, &model.ChannelDegradationSample{}, &model.SystemTask{}, &model.SystemTaskLock{}))
	enabled := true
	require.NoError(t, db.Create(&model.Model{ModelName: "model", DegradationCheckEnabled: &enabled}).Error)
	t.Setenv("DEGRADATION_CHECK_ENABLED", "false")
	RegisterChannelDegradationTask()
	runSystemTaskScheduler()
	var count int64
	require.NoError(t, db.Model(&model.SystemTask{}).Count(&count).Error)
	assert.Zero(t, count)
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		assert.Equal(t, "Bearer allowed-key", r.Header.Get("Authorization"))
		writeDegradationTestStream(w, `{"choices":[{"message":{"content":"original number"}}]}`, "chat")
	}))
	defer server.Close()
	provider := model.Provider{Name: "provider"}
	require.NoError(t, provider.Save())
	base, overrides := server.URL, `{"Authorization":"must-not-be-used"}`
	channels := []model.Channel{
		{Id: 1, Type: 1, Name: "custom", Key: "allowed-key", Models: "model", ProviderID: provider.ID, BaseURL: &base, HeaderOverride: &overrides},
		{Id: 2, Type: 1, Name: "standard", Key: "allowed-key", Models: "model", ProviderID: provider.ID, BaseURL: &base},
		{Id: 3, Type: 1, Name: "disabled", Status: common.ChannelStatusManuallyDisabled, Key: "never-send", Models: "model", ProviderID: provider.ID, BaseURL: &base},
		{Id: 4, Type: constant.ChannelTypeGemini, Name: "unsupported protocol", Key: "never-send", Models: "model", ProviderID: provider.ID, BaseURL: &base},
		{Id: 5, Type: 1, Name: "disabled keys", Key: "never-send", Models: "model", ProviderID: provider.ID, BaseURL: &base, ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeyStatusList: map[int]int{0: common.ChannelStatusManuallyDisabled}}},
	}
	require.NoError(t, db.Create(&channels).Error)
	t.Setenv("DEGRADATION_CHECK_ENABLED", "true")
	t.Setenv("DEGRADATION_CHECK_ALLOW_HTTP", "true")
	t.Setenv("DEGRADATION_CHECK_BATCH_SIZE", "1")
	t.Setenv("DEGRADATION_CHECK_FORMAT", "chat")
	t.Setenv("DEGRADATION_CHECK_TIMEOUT_SECONDS", "30")
	t.Setenv("DEGRADATION_CHECK_INTERVAL_MINUTES", "60")
	RegisterChannelDegradationTask()
	handler := channelDegradationHandler{}
	for index, expected := range []struct{ cursor, normal, skipped int }{{1, 0, 1}, {2, 1, 0}, {5, 0, 1}, {1, 0, 1}} {
		if index > 0 {
			// Advance the persisted scheduling deadline, not the wall clock.
			require.NoError(t, db.Model(&model.SystemTask{}).Where("type = ?", handler.Type()).Update("updated_at", common.GetTimestamp()-86400).Error)
		}
		runSystemTaskScheduler()
		task, err := model.GetActiveSystemTask(handler.Type())
		require.NoError(t, err)
		require.NotNil(t, task)
		claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "runner", common.GetTimestamp()+60)
		require.NoError(t, err)
		require.True(t, ok)
		handler.Run(context.Background(), claimed, "runner")
		finished, err := model.GetSystemTaskByTaskID(task.TaskID)
		require.NoError(t, err)
		require.Equal(t, model.SystemTaskStatusSucceeded, finished.Status)
		var result degradationTaskResult
		require.NoError(t, common.UnmarshalJsonStr(finished.Result, &result))
		assert.Equal(t, expected.cursor, result.Cursor)
		assert.Equal(t, expected.normal, result.Normal)
		assert.Equal(t, expected.skipped, result.Skipped)
		assert.Equal(t, 1, result.Processed)
		// An immediately repeated scheduler pass must not enqueue another job.
		runSystemTaskScheduler()
		active, err := model.GetActiveSystemTask(handler.Type())
		require.NoError(t, err)
		assert.Nil(t, active)
	}
	assert.EqualValues(t, 1, hits.Load())
}

// Shared by both migration shapes on every real database engine.
func testDegradationFullScan(t *testing.T, db *gorm.DB) {
	for _, table := range []any{&model.ChannelDegradationSample{}, &model.SystemTaskLock{}, &model.SystemTask{}, &model.Channel{}, &model.Model{}, &model.Provider{}} {
		require.NoError(t, db.Unscoped().Where("1 = 1").Delete(table).Error)
	}
	policy, err := model.LoadDegradationModelPolicy(context.Background())
	require.NoError(t, err)
	assert.False(t, policy.Enabled("missing-model"), "models without metadata start unchecked")
	enabled := true
	for _, name := range []string{"m-a", "m-b", "m-c", "text-model", "gpt-image-2"} {
		require.NoError(t, db.Create(&model.Model{ModelName: name, DegradationCheckEnabled: &enabled}).Error)
	}
	t.Setenv("DEGRADATION_CHECK_ENABLED", "false")
	previousMaster := common.IsMasterNode
	common.IsMasterNode = false
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	providers := []model.Provider{{Name: "first"}, {Name: "second"}, {Name: "deleted"}}
	for i := range providers {
		require.NoError(t, providers[i].Save())
	}
	var hits atomic.Int32
	var changeChannel atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if id := changeChannel.Swap(0); id > 0 {
			assert.NoError(t, db.Model(&model.Channel{}).Where("id = ?", id).Update("key", "rotated-scan-key-canary").Error)
		}
		assert.Equal(t, "Bearer scan-key-canary", r.Header.Get("Authorization"))
		var body map[string]any
		assert.NoError(t, common.DecodeJson(r.Body, &body))
		assert.Equal(t, []any{map[string]any{"role": "user", "content": DegradationPrompt}}, body["messages"])
		switch body["model"] {
		case "up-b":
			writeDegradationTestStream(w, `{"choices":[{"message":{"content":"16"}}]}`, "chat")
		case "up-c":
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"error":"scan-response-canary"}`)
		case "up-a":
			writeDegradationTestStream(w, `{"choices":[{"message":{"content":"can't scan-response-canary"}}]}`, "chat")
		default:
			assert.Fail(t, "unexpected upstream model")
		}
	}))
	defer server.Close()
	base, mapping, testModel := server.URL, `{"m-a":"up-a","m-b":"up-b","m-c":"up-c"}`, "not-a-configured-model"
	channels := []model.Channel{
		{Type: 1, Name: "mixed", ProviderID: providers[0].ID, Models: " m-b, m-a,m-a,,", TestModel: &testModel},
		{Type: 1, Name: "disabled", ProviderID: providers[0].ID, Models: "m-a", Status: common.ChannelStatusManuallyDisabled},
		{Type: 1, Name: "inconclusive", ProviderID: providers[1].ID, Models: "m-a,m-c"},
		{Type: constant.ChannelTypeGemini, Name: "unsupported", ProviderID: providers[1].ID, Models: "m-a,m-b"},
		{Type: 1, Name: "deleted provider", ProviderID: providers[2].ID, Models: "m-a"},
		{Type: 1, Name: "orphan", ProviderID: 99999999, Models: "m-a"},
	}
	for i := range channels {
		channels[i].BaseURL = &base
		channels[i].Key = "scan-key-canary"
		channels[i].ModelMapping = &mapping
	}
	require.NoError(t, db.Create(&channels).Error)
	require.NoError(t, db.Delete(&providers[2]).Error)
	var preview, stderr bytes.Buffer
	assert.Equal(t, 0, RunDegradationCLI(context.Background(), []string{"--dry-run"}, &preview, &stderr), stderr.String())
	assert.Zero(t, hits.Load())
	var count int64
	require.NoError(t, db.Model(&model.SystemTask{}).Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, db.Model(&model.ChannelDegradationSample{}).Count(&count).Error)
	assert.Zero(t, count)

	var output bytes.Buffer
	assert.Equal(t, 2, RunDegradationCLI(context.Background(), []string{"--allow-http"}, &output, &stderr), stderr.String())
	assert.EqualValues(t, 5, hits.Load())
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	require.Len(t, lines, 8)
	var summary DegradationScanSummary
	require.NoError(t, common.UnmarshalJsonStr(lines[len(lines)-1], &summary))
	assert.Equal(t, 7, summary.Targets)
	assert.Equal(t, 4, summary.Channels)
	assert.Equal(t, 3, summary.Normal)
	assert.Equal(t, 1, summary.Abnormal)
	assert.Equal(t, 1, summary.Errors)
	assert.Equal(t, 2, summary.Skipped)
	assert.Equal(t, 4, summary.SnapshotApplied)
	for _, secret := range []string{"scan-key-canary", "scan-response-canary", server.URL} {
		assert.NotContains(t, output.String()+stderr.String(), secret)
	}
	var samples []model.ChannelDegradationSample
	require.NoError(t, db.Where("task_id = ?", summary.TaskID).Find(&samples).Error)
	require.Len(t, samples, 5)
	for _, sample := range samples {
		assert.Equal(t, sample.ChannelID != channels[1].Id, sample.SnapshotApplied)
	}
	var mixed, inconclusive, disabled model.Channel
	require.NoError(t, db.First(&mixed, channels[0].Id).Error)
	require.NotNil(t, mixed.ModelConfig("m-b").IsDegraded)
	assert.True(t, *mixed.ModelConfig("m-b").IsDegraded)
	require.NotNil(t, mixed.ModelConfig("m-a").IsDegraded)
	assert.False(t, *mixed.ModelConfig("m-a").IsDegraded)
	for _, sample := range samples {
		if sample.ChannelID == mixed.Id {
			assert.Equal(t, sample.TTFTMs, mixed.ModelConfig(sample.Model).TTFTMs)
		}
	}
	require.NoError(t, db.First(&inconclusive, channels[2].Id).Error)
	assert.Nil(t, inconclusive.ModelConfig("m-c").IsDegraded)
	require.NotNil(t, inconclusive.ModelConfig("m-c").IsOnline)
	assert.False(t, *inconclusive.ModelConfig("m-c").IsOnline)
	assert.True(t, *inconclusive.ModelConfig("m-a").IsOnline)
	require.NoError(t, db.First(&disabled, channels[1].Id).Error)
	assert.Empty(t, disabled.ModelConfigs)
	assert.Equal(t, common.ChannelStatusManuallyDisabled, disabled.Status)
	for _, scenario := range []struct {
		name                    string
		args                    []string
		targets, requests, exit int
	}{
		{"provider", []string{"--provider-id", strconv.Itoa(providers[0].ID)}, 3, 3, 1},
		{"channel", []string{"--channel-id", strconv.Itoa(channels[0].Id)}, 2, 2, 1},
		{"intersection", []string{"--provider-id", strconv.Itoa(providers[0].ID), "--channel-id", strconv.Itoa(channels[0].Id), "--model", "m-a"}, 1, 1, 0},
		{"dimension mismatch", []string{"--provider-id", strconv.Itoa(providers[1].ID), "--channel-id", strconv.Itoa(channels[0].Id)}, 0, 0, 2},
		{"model missing", []string{"--model", "not-a-configured-model"}, 0, 0, 2},
		{"model exact not wildcard", []string{"--model", "m-%"}, 0, 0, 2},
		{"repeated ids and model dedup", []string{"--channel-id", strconv.Itoa(channels[0].Id) + "," + strconv.Itoa(channels[1].Id), "--channel-id", strconv.Itoa(channels[0].Id), "--model", "m-a,m-a"}, 2, 2, 0},
		{"disabled", []string{"--status", "disabled"}, 1, 1, 0},
		{"enabled provider", []string{"--provider-id", strconv.Itoa(providers[0].ID), "--status", "enabled"}, 2, 2, 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			before := hits.Load()
			var out, stderr bytes.Buffer
			assert.Equal(t, scenario.exit, RunDegradationCLI(context.Background(), append(scenario.args, "--allow-http"), &out, &stderr), stderr.String())
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			var result DegradationScanSummary
			require.NoError(t, common.UnmarshalJsonStr(lines[len(lines)-1], &result))
			assert.Equal(t, scenario.targets, result.Targets)
			assert.EqualValues(t, scenario.requests, hits.Load()-before)
			if slices.Contains(scenario.args, "--model") && scenario.requests > 0 {
				assert.Positive(t, result.SnapshotApplied)
			}
		})
	}
	for _, args := range [][]string{{"--provider-id", "0"}, {"--provider-id", "1,,2"}, {"--channel-id", "-1"}, {"--model", ""}, {"--status", "bad"}, {"--concurrency", "0"}, {"--concurrency", "65"}, {"--unknown-secret-canary"}} {
		var out, stderr bytes.Buffer
		before := hits.Load()
		assert.Equal(t, 2, RunDegradationCLI(context.Background(), args, &out, &stderr))
		assert.Equal(t, before, hits.Load())
		assert.NotContains(t, stderr.String(), "unknown-secret-canary")
	}
	// Editing a channel mid-request prevents stale model observations.
	require.NoError(t, db.First(&mixed, channels[0].Id).Error)
	originalConfigs := mixed.ModelConfigs
	changeChannel.Store(int64(channels[0].Id))
	var changedOut, changedErr bytes.Buffer
	assert.Equal(t, 1, RunDegradationCLI(context.Background(), []string{"--channel-id", strconv.Itoa(channels[0].Id), "--allow-http"}, &changedOut, &changedErr), changedErr.String())
	var changed model.Channel
	require.NoError(t, db.First(&changed, channels[0].Id).Error)
	assert.Equal(t, originalConfigs, changed.ModelConfigs)
	require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", channels[0].Id).Update("key", "scan-key-canary").Error)
	// Real lease contention: neither another CLI nor a periodic worker may
	// replace this scan's filters or double its requests.
	owned, err := model.BeginDegradationScan(context.Background(), "owner", nil, systemTaskLockUntil())
	require.NoError(t, err)
	var blocked, blockedErr bytes.Buffer
	before := hits.Load()
	assert.Equal(t, 2, RunDegradationCLI(context.Background(), nil, &blocked, &blockedErr))
	assert.Equal(t, before, hits.Load())
	require.NoError(t, db.Model(&model.SystemTaskLock{}).Where("task_id = ?", owned.TaskID).Update("locked_until", common.GetTimestamp()-1).Error)
	var recovered, recoverErr bytes.Buffer
	assert.Equal(t, 0, RunDegradationCLI(context.Background(), []string{"--channel-id", strconv.Itoa(channels[1].Id), "--allow-http"}, &recovered, &recoverErr), recoverErr.String())
	expired, err := model.GetSystemTaskByTaskID(owned.TaskID)
	require.NoError(t, err)
	assert.Equal(t, model.SystemTaskStatusFailed, expired.Status)
	late := model.ChannelDegradationSample{ID: uuid.NewString(), TaskID: owned.TaskID, State: "abnormal", StartedAt: time.Now().UnixMilli(), CompletedAt: time.Now().UnixMilli()}
	assert.ErrorIs(t, model.RecordChannelDegradation(context.Background(), &channels[0], &late, "owner", true), model.ErrSystemTaskLockLost)
	pending, err := model.CreateSystemTask(model.SystemTaskTypeChannelDegradation, nil, nil)
	require.NoError(t, err)
	before = hits.Load()
	blocked.Reset()
	blockedErr.Reset()
	assert.Equal(t, 2, RunDegradationCLI(context.Background(), nil, &blocked, &blockedErr))
	assert.Equal(t, before, hits.Load())
	stillPending, err := model.GetSystemTaskByTaskID(pending.TaskID)
	require.NoError(t, err)
	assert.Equal(t, model.SystemTaskStatusPending, stillPending.Status)
	require.NoError(t, db.Where("id = ?", pending.ID).Delete(&model.SystemTask{}).Error)
	// Model metadata controls every matching channel model. Provider/channel
	// opt-outs win over an enabled model and even explicit CLI filters.
	var metadata model.Model
	require.NoError(t, db.Where("model_name = ?", "m-b").First(&metadata).Error)
	assert.True(t, *metadata.DegradationCheckEnabled)
	require.NoError(t, db.Model(&model.Model{}).Where("id = ?", metadata.Id).Update("degradation_check_enabled", false).Error)
	policy, err = model.LoadDegradationModelPolicy(context.Background())
	require.NoError(t, err)
	assert.False(t, policy.Enabled("m-b"))
	assert.True(t, policy.Enabled("m-a"))
	var configured model.Channel
	require.NoError(t, db.First(&configured, channels[0].Id).Error)
	_, chosen, reason := channelDegradationTarget(&configured, DegradationTaskConfig{Timeout: time.Second, AllowHTTP: true, Format: "chat"}, "", policy.Enabled)
	assert.Empty(t, reason)
	assert.Equal(t, "m-a", chosen)
	var onlyDisabled, disabledErr bytes.Buffer
	before = hits.Load()
	assert.Equal(t, 2, RunDegradationCLI(context.Background(), []string{"--channel-id", strconv.Itoa(channels[0].Id), "--model", "m-b", "--allow-http"}, &onlyDisabled, &disabledErr))
	assert.Equal(t, before, hits.Load())
	assert.Contains(t, onlyDisabled.String(), `"reason":"model_check_disabled"`)
	skipProvider := true
	require.NoError(t, providers[0].Save(&skipProvider))
	providers[0].Name = "renamed"
	require.NoError(t, providers[0].Save())
	var savedProvider model.Provider
	require.NoError(t, db.First(&savedProvider, providers[0].ID).Error)
	assert.True(t, savedProvider.SkipDegradationCheck)
	var optedOut, optedErr bytes.Buffer
	before = hits.Load()
	assert.Equal(t, 2, RunDegradationCLI(context.Background(), []string{"--provider-id", strconv.Itoa(providers[0].ID), "--channel-id", strconv.Itoa(channels[0].Id), "--model", "m-a", "--allow-http"}, &optedOut, &optedErr))
	assert.Equal(t, before, hits.Load())
	assert.Contains(t, optedOut.String(), `"targets":0`)
	skipProvider = false
	require.NoError(t, providers[0].Save(&skipProvider))
	assert.False(t, configured.SkipDegradationCheck, "channel defaults to not disabled")
	require.NoError(t, model.SetChannelDegradationExcluded(channels[0].Id, true))
	var channelExcluded, channelErr bytes.Buffer
	before = hits.Load()
	assert.Equal(t, 2, RunDegradationCLI(context.Background(), []string{"--channel-id", strconv.Itoa(channels[0].Id), "--model", "m-a", "--allow-http"}, &channelExcluded, &channelErr))
	assert.Equal(t, before, hits.Load())
	assert.Contains(t, channelExcluded.String(), `"targets":0`)
	configured.Name = "renamed without resetting quality policy"
	require.NoError(t, configured.Save())
	var reloaded model.Channel
	require.NoError(t, db.First(&reloaded, configured.Id).Error)
	assert.True(t, reloaded.SkipDegradationCheck)
	patch := model.Channel{Id: configured.Id, Name: "updated"}
	require.NoError(t, patch.Update())
	require.NoError(t, db.First(&reloaded, configured.Id).Error)
	assert.True(t, reloaded.SkipDegradationCheck)
	require.NoError(t, model.SetChannelDegradationExcluded(channels[0].Id, false))
	checkOff := false
	pattern := model.Model{ModelName: "m-", NameRule: model.NameRulePrefix, DegradationCheckEnabled: &checkOff}
	require.NoError(t, db.Create(&pattern).Error)
	policy, err = model.LoadDegradationModelPolicy(context.Background())
	require.NoError(t, err)
	assert.True(t, policy.Enabled("m-a"))
	assert.False(t, policy.Enabled("m-unlisted"), "disabled prefix covers matching models without exact metadata")
	assert.False(t, policy.Enabled("m-b"))
	require.NoError(t, db.Model(&model.Model{}).Where("id = ?", metadata.Id).Update("degradation_check_enabled", true).Error)
	policy, err = model.LoadDegradationModelPolicy(context.Background())
	require.NoError(t, err)
	assert.True(t, policy.Enabled("m-a"))
	assert.False(t, policy.Enabled("m-unlisted"))
	assert.True(t, policy.Enabled("m-b"), "exact metadata takes precedence over a disabled prefix")
	require.NoError(t, db.Unscoped().Delete(&pattern).Error)
	// Cross the actual query page boundary: one invocation must visit all pages.
	more := make([]model.Channel, 101)
	for i := range more {
		more[i] = model.Channel{Type: 1, ProviderID: providers[0].ID, Models: "m-a", Key: "scan-key-canary", BaseURL: &base, ModelMapping: &mapping}
	}
	require.NoError(t, db.Create(&more).Error)
	var paged, pageErr bytes.Buffer
	before = hits.Load()
	assert.Equal(t, 0, RunDegradationCLI(context.Background(), []string{"--provider-id", strconv.Itoa(providers[0].ID), "--model", "m-a", "--allow-http"}, &paged, &pageErr), pageErr.String())
	assert.EqualValues(t, 103, hits.Load()-before)
	var rows []model.ChannelDegradationSample
	require.NoError(t, db.Where("channel_id = ?", more[100].Id).Find(&rows).Error)
	require.Len(t, rows, 1)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	before = hits.Load()
	var cancelledOut, cancelledErr bytes.Buffer
	assert.Equal(t, 2, RunDegradationCLI(cancelled, []string{"--allow-http"}, &cancelledOut, &cancelledErr))
	assert.Equal(t, before, hits.Load())
	t.Run("advanced custom in actual CLI scan", func(t *testing.T) {
		var calls atomic.Int32
		endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			assert.Equal(t, "/custom/text-model", r.URL.Path)
			assert.Equal(t, "secret-canary", r.Header.Get("X-Api-Key"))
			assert.Empty(t, r.Header.Get("Authorization"))
			writeDegradationTestStream(w, `{"choices":[{"message":{"content":"can't"}}]}`, "chat")
		}))
		defer endpoint.Close()
		address := endpoint.URL
		advanced := model.Channel{ProviderID: providers[0].ID, Type: constant.ChannelTypeAdvancedCustom, Models: "text-model,gpt-image-2", Key: "secret-canary", BaseURL: &address}
		other := dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/chat/completions", UpstreamPath: "/custom/{model}", Auth: &dto.AdvancedCustomRouteAuth{Type: dto.AdvancedCustomAuthTypeHeader, Name: "X-Api-Key", Value: "{api_key}"}}}}}
		encoded, err := common.Marshal(other)
		require.NoError(t, err)
		advanced.OtherSettings = string(encoded)
		require.NoError(t, advanced.Insert())
		var out, stderr bytes.Buffer
		assert.Equal(t, 2, RunDegradationCLI(context.Background(), []string{"--channel-id", strconv.Itoa(advanced.Id), "--allow-http"}, &out, &stderr))
		assert.EqualValues(t, 1, calls.Load())
		assert.Contains(t, out.String(), `"reason":"unsupported_model_modality"`)
		assert.NotContains(t, out.String()+stderr.String(), "secret-canary")
		var history []model.ChannelDegradationSample
		require.NoError(t, db.Where("channel_id = ?", advanced.Id).Find(&history).Error)
		require.Len(t, history, 1)
		assert.Equal(t, "text-model", history[0].Model)
	})
	// Exercise the CLI-specific connection opener on each actual database, not
	// only a test-supplied GORM handle. Opening must not migrate or create users.
	switch db.Dialector.Name() {
	case "sqlite":
		t.Setenv("SQL_DSN", "local")
		t.Setenv("SQLITE_PATH", db.Dialector.(*sqlite.Dialector).DSN)
	case "mysql":
		t.Setenv("SQL_DSN", os.Getenv("TEST_MYSQL_DSN"))
	case "postgres":
		t.Setenv("SQL_DSN", os.Getenv("TEST_POSTGRES_DSN"))
	}
	closeDB, err := model.OpenDegradationDatabase()
	require.NoError(t, err)
	var openedOut, openedErr bytes.Buffer
	assert.Equal(t, 0, RunDegradationCLI(context.Background(), []string{"--provider-id", strconv.Itoa(providers[0].ID), "--channel-id", strconv.Itoa(channels[0].Id), "--model", "m-a", "--allow-http"}, &openedOut, &openedErr), openedErr.String())
	require.NoError(t, closeDB())
	model.DB = db
}

func TestDegradationDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "degradation.db"))
			case "mysql":
				if os.Getenv("TEST_MYSQL_DSN") == "" {
					t.Skip("TEST_MYSQL_DSN required for real MySQL verification")
				}
				config, err := mysqldriver.ParseDSN(os.Getenv("TEST_MYSQL_DSN"))
				require.NoError(t, err)
				require.Equal(t, "zzz_degradation_test", config.DBName, "this destructive matrix requires its dedicated database")
				driver = mysql.Open(os.Getenv("TEST_MYSQL_DSN"))
			case "postgres":
				if os.Getenv("TEST_POSTGRES_DSN") == "" {
					t.Skip("TEST_POSTGRES_DSN required for real PostgreSQL verification")
				}
				config, err := pgx.ParseConfig(os.Getenv("TEST_POSTGRES_DSN"))
				require.NoError(t, err)
				require.Equal(t, "zzz_degradation_test", config.Database, "this destructive matrix requires its dedicated database")
				driver = postgres.New(postgres.Config{DSN: os.Getenv("TEST_POSTGRES_DSN"), PreferSimpleProtocol: true})
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			previousDB, previousType, previousMaster := model.DB, common.MainDatabaseType(), common.IsMasterNode
			model.DB, common.IsMasterNode = db, true
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			t.Cleanup(func() {
				model.DB = previousDB
				common.SetMainDatabaseType(previousType)
				common.IsMasterNode = previousMaster
				require.NoError(t, sqlDB.Close())
			})
			var version string
			versionQuery := "SELECT version()"
			if dialect == "sqlite" {
				versionQuery = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
			t.Logf("database version: %s", version)
			for _, mode := range []string{"fresh", "upgrade"} {
				t.Run(mode, func(t *testing.T) {
					t.Cleanup(func() {
						require.NoError(t, db.Migrator().DropTable(&model.ChannelDegradationSample{}, &model.SystemTaskLock{}, &model.SystemTask{}, &model.Ability{}, &model.Channel{}, &model.Model{}, &model.Provider{}))
					})
					require.NoError(t, db.AutoMigrate(&model.Provider{}, &model.Model{}, &model.Channel{}, &model.Ability{}, &model.SystemTask{}, &model.SystemTaskLock{}))
					provider := model.Provider{Name: "provider"}
					require.NoError(t, provider.Save())
					legacy := model.Channel{Name: "legacy", Key: "private-key-canary", Type: constant.ChannelTypeOpenAI, Models: "alias", Group: "default", ProviderID: provider.ID}
					var legacyHistory *model.ChannelDegradationSample
					if mode == "upgrade" {
						require.NoError(t, db.Create(&model.Model{ModelName: "legacy-model"}).Error)
						require.NoError(t, db.Migrator().DropIndex(&model.Channel{}, "InstanceID"))
						require.NoError(t, db.Migrator().DropColumn(&model.Channel{}, "InstanceID"))
						require.NoError(t, db.Migrator().DropColumn(&model.Channel{}, "SkipDegradationCheck"))
						require.NoError(t, db.Omit("InstanceID", "SkipDegradationCheck").Create(&legacy).Error)
						require.NoError(t, db.Migrator().DropColumn(&model.Provider{}, "SkipDegradationCheck"))
						require.NoError(t, db.Migrator().DropColumn(&model.Model{}, "DegradationCheckEnabled"))
						require.NoError(t, db.AutoMigrate(&model.ChannelDegradationSample{}))
						require.NoError(t, db.Migrator().DropColumn(&model.ChannelDegradationSample{}, "TTFTMs"))
						legacyHistory = &model.ChannelDegradationSample{ID: uuid.NewString(), State: "normal", DurationMs: 250, StartedAt: 1, CompletedAt: 251}
						require.NoError(t, db.Omit("TTFTMs").Create(legacyHistory).Error)
					} else {
						require.NoError(t, legacy.Insert())
					}
					for range 2 {
						require.NoError(t, db.AutoMigrate(&model.Provider{}, &model.Model{}, &model.Channel{}, &model.ChannelDegradationSample{}))
						require.NoError(t, model.InitializeChannelInstances(db))
					}
					if mode == "upgrade" {
						var migrated model.ChannelDegradationSample
						require.NoError(t, db.First(&migrated, "id = ?", legacyHistory.ID).Error)
						assert.Equal(t, "normal", migrated.State)
						assert.EqualValues(t, 250, migrated.DurationMs)
						assert.Nil(t, migrated.TTFTMs, "legacy total time must not be migrated into TTFT")
						policy, err := model.LoadDegradationModelPolicy(context.Background())
						require.NoError(t, err)
						assert.False(t, policy.Enabled("legacy-model"), "migration must leave existing models unchecked")
						var existing model.Provider
						require.NoError(t, db.First(&existing, provider.ID).Error)
						assert.False(t, existing.SkipDegradationCheck, "existing providers stay eligible")
					}
					enabled := true
					require.NoError(t, db.Create(&model.Model{ModelName: "alias", DegradationCheckEnabled: &enabled}).Error)
					var source model.Channel
					require.NoError(t, db.First(&source, legacy.Id).Error)
					require.NotNil(t, source.InstanceID)
					instance := *source.InstanceID
					assert.Equal(t, legacy.Key, source.Key)
					source.Name = "renamed"
					require.NoError(t, source.Save())
					assert.Equal(t, instance, *source.InstanceID)
					require.True(t, db.Migrator().HasIndex(&model.Channel{}, "InstanceID"))
					forged := uuid.NewString()
					patch := model.Channel{Id: source.Id, Name: "identity preserved", InstanceID: &forged}
					require.NoError(t, patch.Update())
					assert.Equal(t, instance, *patch.InstanceID)
					require.NoError(t, model.InitializeChannelInstances(db))
					clone := source
					clone.Id = 0
					require.NoError(t, clone.Insert())
					assert.NotEqual(t, instance, *clone.InstanceID)
					require.NoError(t, clone.Delete())

					t.Setenv("DEGRADATION_CHECK_ENABLED", "true")
					t.Setenv("DEGRADATION_CHECK_ALLOW_HTTP", "true")
					t.Setenv("DEGRADATION_CHECK_BATCH_SIZE", "1")
					t.Setenv("DEGRADATION_CHECK_FORMAT", "chat")
					t.Setenv("DEGRADATION_CHECK_TIMEOUT_SECONDS", "30")
					t.Setenv("DEGRADATION_CHECK_INTERVAL_MINUTES", "60")
					var calls atomic.Int32
					var responseMode atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						assert.Equal(t, "/v1/chat/completions", r.URL.Path)
						assert.Equal(t, "Bearer private-enabled-canary", r.Header.Get("Authorization"))
						body, _ := io.ReadAll(r.Body)
						var request map[string]any
						assert.NoError(t, common.Unmarshal(body, &request))
						assert.Equal(t, "upstream-model", request["model"])
						switch responseMode.Load() {
						case 1:
							writeDegradationTestStream(w, `{"choices":[{"message":{"content":"16"}}]}`, "chat")
						case 2:
							w.WriteHeader(429)
							_, _ = io.WriteString(w, `{"error":"can't private-response-canary"}`)
						case 3:
							assert.NoError(t, db.Model(&model.Channel{}).Where("id = ?", source.Id).Update("key", "rotated-private-canary").Error)
							writeDegradationTestStream(w, `{"choices":[{"message":{"content":"16"}}]}`, "chat")
						default:
							writeDegradationTestStream(w, `{"choices":[{"message":{"content":"can’t private-response-canary"}}]}`, "chat")
						}
					}))
					defer server.Close()
					base, mapping, testModel, ttft := server.URL, `{"alias":"middle","middle":"upstream-model"}`, "alias", int64(25)
					source.BaseURL, source.ModelMapping, source.TestModel = &base, &mapping, &testModel
					source.ModelConfigs = model.ChannelModelConfigs{"alias": {TTFTMs: &ttft}}
					source.Models = "other-model,alias"
					source.Key = "disabled-private-canary\nprivate-enabled-canary"
					source.ChannelInfo = model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyMode: constant.MultiKeyModePolling, MultiKeyPollingIndex: 1, MultiKeyStatusList: map[int]int{0: common.ChannelStatusManuallyDisabled}}
					require.NoError(t, source.Save())
					handler := channelDegradationHandler{}
					for _, state := range []int32{0, 1, 2, 3} {
						responseMode.Store(state)
						if state == 3 {
							var stored model.Channel
							require.NoError(t, db.First(&stored, source.Id).Error)
							cfg := stored.ModelConfig("alias")
							cfg.IsDegraded = common.GetPointer(false)
							stored.ModelConfigs["alias"] = cfg
							require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", source.Id).Update("model_configs", stored.ModelConfigs).Error)
						}
						task, err := model.CreateSystemTask(handler.Type(), handler.NewPayload(), nil)
						require.NoError(t, err)
						claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "runner", common.GetTimestamp()+60)
						require.NoError(t, err)
						require.True(t, ok)
						_, duplicate, err := model.ClaimSystemTask(task.ID, task.Type, "other-runner", common.GetTimestamp()+60)
						require.NoError(t, err)
						assert.False(t, duplicate)
						invalidLease := model.ChannelDegradationSample{ID: uuid.NewString(), TaskID: task.TaskID, State: "abnormal", StartedAt: time.Now().UnixMilli(), CompletedAt: time.Now().UnixMilli()}
						assert.ErrorIs(t, model.RecordChannelDegradation(context.Background(), &source, &invalidLease, "other-runner"), model.ErrSystemTaskLockLost)
						handler.Run(context.Background(), claimed, "runner")
						finished, err := model.GetSystemTaskByTaskID(task.TaskID)
						require.NoError(t, err)
						require.Equal(t, model.SystemTaskStatusSucceeded, finished.Status, finished.Error)
						var sample model.ChannelDegradationSample
						require.NoError(t, db.Where("task_id = ?", task.TaskID).First(&sample).Error)
						assert.Equal(t, instance, sample.ChannelInstanceID)
						assert.Equal(t, "alias", sample.Model)
						assert.Equal(t, "upstream-model", sample.UpstreamModel)
						assert.Equal(t, "chat", sample.Protocol)
						assert.Equal(t, 512, sample.OutputTokenLimit)
						assert.EqualValues(t, 30000, sample.TimeoutMs)
						encoded, err := common.Marshal(sample)
						require.NoError(t, err)
						for _, secret := range []string{server.URL, "private-enabled-canary", "private-response-canary", "rotated-private-canary"} {
							assert.NotContains(t, string(encoded), secret)
						}
						var current model.Channel
						require.NoError(t, db.First(&current, source.Id).Error)
						if state < 2 {
							require.NotNil(t, sample.TTFTMs)
							assert.GreaterOrEqual(t, *sample.TTFTMs, int64(0))
							assert.LessOrEqual(t, *sample.TTFTMs, sample.DurationMs)
							ttft = *sample.TTFTMs
						}
						config := current.ModelConfig("alias")
						assert.Equal(t, &ttft, config.TTFTMs)
						assert.Equal(t, common.ChannelStatusEnabled, current.Status)
						assert.Equal(t, 1, current.ChannelInfo.MultiKeyPollingIndex)
						switch state {
						case 0:
							assert.Equal(t, "normal", sample.State)
							require.NotNil(t, config.IsDegraded)
							assert.False(t, *config.IsDegraded)
							assert.True(t, sample.SnapshotApplied)
						case 1:
							assert.Equal(t, "abnormal", sample.State)
							require.NotNil(t, config.IsDegraded)
							assert.True(t, *config.IsDegraded)
						case 2:
							assert.Equal(t, "error", sample.State)
							assert.Nil(t, config.IsDegraded)
							require.NotNil(t, config.IsOnline)
							assert.False(t, *config.IsOnline)
						case 3:
							assert.False(t, sample.SnapshotApplied)
							require.NotNil(t, config.IsDegraded)
							assert.False(t, *config.IsDegraded)
						}
					}
					assert.EqualValues(t, 4, calls.Load())
					require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", source.Id).Update("key", source.Key).Error)
					t.Setenv("DEGRADATION_CHECK_ENABLED", "false")
					task, err := model.CreateSystemTask(handler.Type(), nil, nil)
					require.NoError(t, err)
					claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, "runner", common.GetTimestamp()+60)
					require.NoError(t, err)
					require.True(t, ok)
					handler.Run(context.Background(), claimed, "runner")
					assert.EqualValues(t, 4, calls.Load())
					for _, check := range []struct {
						name, state      string
						sampleTTFT, want int64
						edit             bool
					}{
						{"measured zero", "normal", 0, 0, false},
						{"failed stream preserves successful latency", "error", 7, 0, false},
						{"in-flight manual edit wins", "normal", 7, 99, true},
					} {
						t.Run(check.name, func(t *testing.T) {
							var captured model.Channel
							require.NoError(t, db.First(&captured, source.Id).Error)
							if check.edit {
								cfg := captured.ModelConfig("alias")
								cfg.TTFTMs = &check.want
								require.NoError(t, db.Model(&model.Channel{}).Where("id = ?", source.Id).Update("model_configs", model.ChannelModelConfigs{"alias": cfg}).Error)
							}
							when := time.Now().UnixMilli()
							measured := model.ChannelDegradationSample{ID: uuid.NewString(), Model: "alias", State: check.state, StartedAt: when, CompletedAt: when, DurationMs: 10, TTFTMs: &check.sampleTTFT}
							require.NoError(t, model.RecordChannelDegradation(context.Background(), &captured, &measured, ""))
							assert.Equal(t, !check.edit, measured.SnapshotApplied)
							var actual model.Channel
							require.NoError(t, db.First(&actual, source.Id).Error)
							require.NotNil(t, actual.ModelConfig("alias").TTFTMs)
							assert.Equal(t, check.want, *actual.ModelConfig("alias").TTFTMs)
							invalid := int64(-1)
							measured.ID = uuid.NewString()
							measured.TTFTMs = &invalid
							assert.Error(t, model.RecordChannelDegradation(context.Background(), &actual, &measured, ""))
						})
					}
					// Old observations stay separate when a numeric channel ID is reused.
					var beforeDelete model.Channel
					require.NoError(t, db.First(&beforeDelete, source.Id).Error)
					require.NoError(t, beforeDelete.Delete())
					replacement := legacy
					replacement.InstanceID = nil
					require.NoError(t, replacement.Insert())
					assert.Equal(t, legacy.Id, replacement.Id)
					assert.NotEqual(t, instance, *replacement.InstanceID)
					late := model.ChannelDegradationSample{ID: uuid.NewString(), RuleVersion: DegradationRuleVersion, State: "abnormal", StartedAt: time.Now().UnixMilli(), CompletedAt: time.Now().UnixMilli()}
					require.NoError(t, model.RecordChannelDegradation(context.Background(), &beforeDelete, &late, ""))
					assert.False(t, late.SnapshotApplied)
					assert.Error(t, model.RecordChannelDegradation(context.Background(), &beforeDelete, &late, ""), "a duplicate UUID cannot rewrite history")
					var currentReplacement model.Channel
					require.NoError(t, db.First(&currentReplacement, replacement.Id).Error)
					assert.Nil(t, currentReplacement.IsDegraded)
					var count int64
					require.NoError(t, db.Model(&model.ChannelDegradationSample{}).Where("channel_instance_id = ?", instance).Count(&count).Error)
					assert.EqualValues(t, 8, count)
					require.NoError(t, db.Where("type = ?", handler.Type()).Delete(&model.SystemTask{}).Error)
					require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.ChannelDegradationSample{}))
					require.NoError(t, model.InitializeChannelInstances(db))
					require.NoError(t, db.Model(&model.ChannelDegradationSample{}).Count(&count).Error)
					expectedCount := 8
					if legacyHistory != nil {
						expectedCount++
					}
					assert.EqualValues(t, expectedCount, count)
					t.Run("full CLI scan", func(t *testing.T) { testDegradationFullScan(t, db) })
				})
			}
		})
	}
}
