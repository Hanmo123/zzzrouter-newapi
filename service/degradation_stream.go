package service

import (
	"bufio"
	"context"
	"io"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// Read SSE incrementally. TTFT starts at dispatch and ends at the first
// non-empty assistant text/refusal delta, not headers, role or thinking events.
func readDegradationStream(ctx context.Context, body io.Reader, format string, elapsed func() time.Duration) (string, *int64, string) {
	limited := &io.LimitedReader{R: body, N: degradationResponseLimit + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), degradationResponseLimit+1)
	var text, data strings.Builder
	var ttft *int64
	event := ""
	seen, complete, done := false, false, false
	appendText := func(value string) {
		seen = true
		if value != "" && ttft == nil {
			ms := elapsed().Milliseconds()
			ttft = &ms
		}
		text.WriteString(value)
	}
	consume := func() string {
		raw := strings.TrimSuffix(data.String(), "\n")
		if raw == "" {
			return ""
		}
		if format == "chat" && raw == "[DONE]" {
			complete = true
			done = true
			return ""
		}
		if !gjson.Valid(raw) {
			return "invalid_response"
		}
		item := gjson.Parse(raw)
		if e := item.Get("error"); e.Exists() && e.Type != gjson.Null {
			return "stream_error"
		}
		kind := item.Get("type").String()
		if kind == "" {
			kind = event
		}
		if kind == "error" || kind == "response.failed" || kind == "response.incomplete" {
			return "stream_error"
		}
		switch format {
		case "chat":
			for _, choice := range item.Get("choices").Array() {
				if choice.Get("index").Int() != 0 {
					continue
				}
				role := choice.Get("delta.role").String()
				if role != "" && role != "assistant" {
					return "invalid_response"
				}
				content := choice.Get("delta.content")
				if content.Type == gjson.String {
					appendText(content.String())
				} else if content.IsArray() {
					for _, part := range content.Array() {
						if part.Get("type").String() == "text" {
							appendText(part.Get("text").String())
						}
					}
				}
				if refusal := choice.Get("delta.refusal"); refusal.Type == gjson.String {
					appendText(refusal.String())
				}
				if reason := choice.Get("finish_reason"); reason.Type == gjson.String && reason.String() != "" {
					complete = true
				}
			}
		case "responses":
			switch kind {
			case "response.output_text.delta", "response.refusal.delta":
				delta := item.Get("delta")
				if delta.Type != gjson.String {
					return "invalid_response"
				}
				appendText(delta.String())
			case "response.completed":
				if err := item.Get("response.error"); err.Exists() && err.Type != gjson.Null {
					return "stream_error"
				}
				status := item.Get("response.status").String()
				if status != "" && status != "completed" {
					return "stream_error"
				}
				// Some compatible endpoints send only a final snapshot. It can supply a
				// verdict, but cannot supply TTFT; do not label completion time as TTFT.
				if !seen {
					if final, ok := degradationResponseText([]byte(item.Get("response").Raw), "responses"); ok {
						text.WriteString(final)
						seen = true
					}
				}
				complete, done = true, true
			}
		case "anthropic":
			switch kind {
			case "message_start":
				if role := item.Get("message.role").String(); role != "" && role != "assistant" {
					return "invalid_response"
				}
			case "content_block_start":
				if item.Get("content_block.type").String() == "text" {
					appendText(item.Get("content_block.text").String())
				}
			case "content_block_delta":
				if item.Get("delta.type").String() == "text_delta" {
					delta := item.Get("delta.text")
					if delta.Type != gjson.String {
						return "invalid_response"
					}
					appendText(delta.String())
				}
			case "message_stop":
				complete, done = true, true
			}
		}
		return ""
	}
	for scanner.Scan() {
		if limited.N <= 0 {
			return "", ttft, "response_too_large"
		}
		line := scanner.Text()
		if line == "" {
			if code := consume(); code != "" {
				return "", ttft, code
			}
			data.Reset()
			event = ""
			if done {
				break
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
		case "data":
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if limited.N <= 0 {
		return "", ttft, "response_too_large"
	}
	if err := scanner.Err(); err != nil {
		return "", ttft, degradationIOError(ctx, err, "read_error")
	}
	if ctx.Err() != nil {
		return "", ttft, degradationIOError(ctx, ctx.Err(), "read_error")
	}
	if !done && data.Len() > 0 {
		if code := consume(); code != "" {
			return "", ttft, code
		}
	}
	if !complete || !seen {
		return "", ttft, "invalid_response"
	}
	return text.String(), ttft, ""
}
