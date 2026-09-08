package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPresetsAndPipedInput(t *testing.T) {
	for _, test := range []struct {
		name, input, prompt         string
		args                        []string
		think                       bool
		temperature, topP, presence float64
	}{
		{"quick", "    print(\"café\")\n", "Explain this\n\nContext:\n    print(\"café\")\n", []string{"Explain", "this"}, false, .7, .8, 1.5},
		{"thinking", "", "Check this", []string{"-t", "Check this"}, true, .6, .95, 0},
		{"flag after question", "", "Check this", []string{"Check this", "--think"}, true, .6, .95, 0},
		{"stdin only", "What is 2+2?\n", "What is 2+2?\n", nil, false, .7, .8, 1.5},
		{"literal flag", "", "-t", []string{"--", "-t"}, false, .7, .8, 1.5},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Error("missing JSON content type")
				}
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				requests <- request
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, ": keepalive\n\ndata: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":null}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"scratch work\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"café\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\" works\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{}}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			t.Setenv("QWEN_URL", server.URL)
			var stdout, stderr bytes.Buffer
			if err := run(context.Background(), test.args, strings.NewReader(test.input), &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			request := <-requests
			for key, expected := range map[string]float64{
				"temperature": test.temperature, "top_p": test.topP, "presence_penalty": test.presence,
				"top_k": 20, "min_p": 0, "repeat_penalty": 1,
			} {
				if request[key] != expected {
					t.Errorf("%s: got %v, want %v", key, request[key], expected)
				}
			}
			message := request["messages"].([]any)[0].(map[string]any)
			if message["role"] != "user" || message["content"] != test.prompt {
				t.Errorf("input lost or changed: %#v", message)
			}
			if request["chat_template_kwargs"].(map[string]any)["enable_thinking"] != test.think {
				t.Error("wrong thinking mode")
			}
			if request["stream"] != true || request["reasoning_format"] != "deepseek" {
				t.Error("streaming or reasoning parsing disabled")
			}
			if request["ignore_eos"] != false {
				t.Error("normal requests should allow the model to finish early")
			}
			if stops := request["stop"].([]any); len(stops) != 2 || stops[0] != "<|im_end|>" || stops[1] != "<|endoftext|>" {
				t.Errorf("missing stop markers: %v", stops)
			}
			if stdout.String() != "café works\n" {
				t.Errorf("unclean answer: %q", stdout.String())
			}
			expectedStatus := ""
			if test.think {
				expectedStatus = "Thinking…\n"
			}
			if stderr.String() != expectedStatus {
				t.Errorf("status: %q", stderr.String())
			}
		})
	}
}

func TestServerURLs(t *testing.T) {
	for _, test := range []struct{ base, expected string }{
		{"", "http://athena:8080/v1/chat/completions"},
		{"http://example:8080/", "http://example:8080/v1/chat/completions"},
		{"http://example:8080/v1", "http://example:8080/v1/chat/completions"},
		{"https://example/api/v1/chat/completions", "https://example/api/v1/chat/completions"},
		{"http://[::1]:8080", "http://[::1]:8080/v1/chat/completions"},
	} {
		got, err := completionURL(test.base)
		if err != nil || got != test.expected {
			t.Errorf("%q: got %q, %v", test.base, got, err)
		}
	}
	for _, bad := range []string{"athena:8080", "ftp://example", "http://", "%"} {
		if _, err := completionURL(bad); err == nil {
			t.Errorf("accepted invalid URL %q", bad)
		}
	}
}

type notifyingWriter struct {
	bytes.Buffer
	first chan struct{}
	once  sync.Once
}

func (w *notifyingWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	w.once.Do(func() { close(w.first) })
	return n, err
}

// Defeat io.WriteString's optimization so writes pass through the notification.
func (w *notifyingWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func TestAnswerStreamsBeforeRequestCompletes(t *testing.T) {
	output := &notifyingWriter{first: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first \"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-output.first:
		case <-time.After(3 * time.Second):
			t.Error("client buffered the answer instead of streaming")
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"second\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("QWEN_URL", server.URL)
	if err := run(context.Background(), []string{"Hello"}, strings.NewReader(""), output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if output.String() != "first second\n" {
		t.Errorf("output: %q", output.String())
	}
}

func TestIncompleteAndFailedStreams(t *testing.T) {
	for _, test := range []struct{ stream, expected string }{
		{"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", "ended before completion"},
		{"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"length\"}]}\n\n", "token limit"},
		{"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n", "no answer"},
		{"data: {\"error\":{\"message\":\"model unavailable\"}}\n\n", "model unavailable"},
		{"data: not JSON\n\n", "invalid response"},
	} {
		var output bytes.Buffer
		err := streamAnswer(strings.NewReader(test.stream), &output, 0)
		if err == nil || !strings.Contains(err.Error(), test.expected) {
			t.Errorf("got %v, want error containing %q", err, test.expected)
		}
	}
}

func TestHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model is loading", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	t.Setenv("QWEN_URL", server.URL)
	var output bytes.Buffer
	err := run(context.Background(), []string{"Hello"}, strings.NewReader(""), &output, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "model is loading") || output.Len() != 0 {
		t.Fatalf("error=%v, stdout=%q", err, output.String())
	}
}

func TestCancellationClosesRequest(t *testing.T) {
	started := make(chan struct{})
	abort := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
		case <-abort:
		}
	}))
	defer server.Close()
	defer close(abort)
	t.Setenv("QWEN_URL", server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"Hello"}, strings.NewReader(""), io.Discard, io.Discard) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request did not cancel")
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"--unknown"}} {
		err := run(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard)
		var badUsage usageError
		if !errors.As(err, &badUsage) {
			t.Errorf("expected usage error, got %v", err)
		}
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--help"}, strings.NewReader(""), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "QWEN_URL") || !strings.Contains(output.String(), defaultURL) {
		t.Error("help omitted server configuration")
	}
}

func TestVersion(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--version"}, strings.NewReader(""), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if output.String() != "qwen "+version+"\n" {
		t.Errorf("unexpected version output: %q", output.String())
	}
}

func TestExactTokenCountFlags(t *testing.T) {
	for _, test := range []struct {
		name  string
		args  []string
		think bool
	}{
		{"separate", []string{"-n", "3", "Hello"}, false},
		{"attached", []string{"-n3", "Hello"}, false},
		{"equals", []string{"-n=3", "Hello"}, false},
		{"after question", []string{"Hello", "-n", "3"}, false},
		{"with thinking", []string{"-t", "-n", "3", "Hello"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				requests <- request
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"some answer\"},\"finish_reason\":\"length\"}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			t.Setenv("QWEN_URL", server.URL)
			var stdout, stderr bytes.Buffer
			if err := run(context.Background(), test.args, strings.NewReader(""), &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			request := <-requests
			if request["max_tokens"] != float64(3) || request["ignore_eos"] != true {
				t.Errorf("exact count was not applied: %#v", request)
			}
			stops, ok := request["stop"].([]any)
			if !ok || len(stops) != 0 {
				t.Errorf("expected an empty stop array, got %#v", request["stop"])
			}
			if request["stream_options"].(map[string]any)["include_usage"] != true {
				t.Error("token usage was not requested")
			}
			if request["chat_template_kwargs"].(map[string]any)["enable_thinking"] != test.think {
				t.Error("-n changed the thinking mode")
			}
			if stdout.String() != "some answer\n" {
				t.Errorf("answer: %q", stdout.String())
			}
			if strings.Contains(stderr.String(), "incomplete") {
				t.Errorf("intentional token cutoff was reported as a failure: %s", stderr.String())
			}
		})
	}
}

func TestInvalidTokenCounts(t *testing.T) {
	for _, args := range [][]string{
		{"-n"}, {"-n", "0"}, {"-n", "-1"}, {"-n", "1.5"},
		{"-n", "abc"}, {"-n="}, {"-n", "2147483648"}, {"-n", "-t"},
	} {
		err := run(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard)
		var badUsage usageError
		if !errors.As(err, &badUsage) || !strings.Contains(err.Error(), "-n") {
			t.Errorf("%v: expected token count usage error, got %v", args, err)
		}
	}
}

func TestExactTokenCountValidation(t *testing.T) {
	for _, test := range []struct{ event, expected string }{
		{`{"choices":[{"delta":{"content":"answer"},"finish_reason":"length"}],"usage":{"completion_tokens":3}}`, ""},
		{`{"choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}],"usage":{"completion_tokens":2}}`, "generated 2 tokens"},
		{`{"choices":[{"delta":{"content":"answer"},"finish_reason":"length"}],"usage":{"completion_tokens":2}}`, "generated 2 tokens"},
		{`{"choices":[{"delta":{"content":"answer"},"finish_reason":"length"}]}`, "did not report a token count"},
		{`{"choices":[{"delta":{"content":"answer"},"finish_reason":"length"}],"usage":{}}`, "did not report a token count"},
		{`{"choices":[{"delta":{"reasoning_content":"thinking"},"finish_reason":"length"}],"usage":{"completion_tokens":3}}`, "before any answer text"},
	} {
		var output bytes.Buffer
		err := streamAnswer(strings.NewReader("data: "+test.event+"\n\ndata: [DONE]\n\n"), &output, 3)
		if test.expected == "" {
			if err != nil {
				t.Errorf("correct token count was rejected: %v", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), test.expected) {
			t.Errorf("expected %q, got %v", test.expected, err)
		}
	}
}
