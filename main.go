package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const defaultURL = "http://athena:8080"

var version = "dev"

const usage = `Usage: qwen [-t] [-n N] [question ...]

Ask Qwen a quick question. Add -t for thinking on harder problems.
Piped input is appended as context, or used as the question if none is given.

  -t, --think   Use the thinking/coding preset
  -n N          Generate exactly N tokens (includes thinking tokens with -t)
  -h, --help    Show this help
      --version Show the installed version

Server: http://athena:8080 (override with QWEN_URL)

Examples:
  qwen "Explain Python slicing"
  qwen -n 100 "Explain Python slicing"
  git diff | qwen -t "Check this for bugs"
  QWEN_URL=http://192.168.1.10:8080 qwen "Hello"
`

type usageError string

func (e usageError) Error() string { return string(e) }

func main() {
	signal.Ignore(syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var input io.Reader = strings.NewReader("")
	info, err := os.Stdin.Stat()
	if err != nil {
		fmt.Fprintln(os.Stderr, "qwen:", err)
		os.Exit(1)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		input = os.Stdin
	}

	err = run(ctx, os.Args[1:], input, os.Stdout, os.Stderr)
	if err == nil || errors.Is(err, syscall.EPIPE) {
		return
	}
	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "\nqwen: cancelled")
		os.Exit(130)
	}
	fmt.Fprintln(os.Stderr, "qwen:", err)
	var badUsage usageError
	if errors.As(err, &badUsage) {
		os.Exit(2)
	}
	os.Exit(1)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	think, parseFlags := false, true
	numTokens := 0
	var words []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if parseFlags {
			switch arg {
			case "--":
				parseFlags = false
				continue
			case "-t", "--think":
				think = true
				continue
			case "-h", "--help":
				_, err := io.WriteString(stdout, usage)
				return err
			case "--version":
				_, err := fmt.Fprintln(stdout, "qwen", version)
				return err
			}
			if strings.HasPrefix(arg, "-n") {
				value := strings.TrimPrefix(strings.TrimPrefix(arg, "-n"), "=")
				if arg == "-n" {
					i++
					if i == len(args) {
						return usageError("-n requires a positive token count")
					}
					value = args[i]
				}
				n, err := strconv.ParseInt(value, 10, 32)
				if err != nil || n <= 0 {
					return usageError("-n requires a positive integer no larger than 2147483647")
				}
				numTokens = int(n)
				continue
			}
			if strings.HasPrefix(arg, "-") && arg != "-" {
				return usageError(fmt.Sprintf("unknown option %q (use qwen --help)", arg))
			}
		}
		words = append(words, arg)
	}

	input, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}
	prompt := strings.Join(words, " ")
	if len(input) > 0 {
		if prompt != "" {
			prompt += "\n\nContext:\n"
		}
		prompt += string(input)
	}
	if strings.TrimSpace(prompt) == "" {
		return usageError("provide a question or pipe input into qwen")
	}

	temperature, topP, presence, maxTokens := 0.7, 0.8, 1.5, 4096
	if think {
		temperature, topP, presence, maxTokens = 0.6, 0.95, 0.0, 8192
	}
	stops := []string{"<|im_end|>", "<|endoftext|>"}
	if numTokens > 0 {
		maxTokens = numTokens
		stops = []string{}
	}
	payload := map[string]any{
		"model":                "qwen3.5",
		"messages":             []map[string]string{{"role": "user", "content": prompt}},
		"temperature":          temperature,
		"top_p":                topP,
		"top_k":                20,
		"min_p":                0.0,
		"presence_penalty":     presence,
		"repeat_penalty":       1.0,
		"max_tokens":           maxTokens,
		"chat_template_kwargs": map[string]bool{"enable_thinking": think},
		"reasoning_format":     "deepseek",
		"stop":                 stops,
		"ignore_eos":           numTokens > 0,
		"stream":               true,
	}
	if numTokens > 0 {
		payload["stream_options"] = map[string]bool{"include_usage": true}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint, err := completionURL(os.Getenv("QWEN_URL"))
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	if think {
		fmt.Fprintln(stderr, "Thinking…")
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", endpoint, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		return fmt.Errorf("HTTP %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	return streamAnswer(response.Body, stdout, numTokens)
}

func completionURL(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		base = defaultURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("QWEN_URL must be an http:// or https:// server URL")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	switch {
	case strings.HasSuffix(u.Path, "/v1/chat/completions"):
	case strings.HasSuffix(u.Path, "/v1"):
		u.Path += "/chat/completions"
	default:
		u.Path += "/v1/chat/completions"
	}
	u.RawPath = ""
	return u.String(), nil
}

func streamAnswer(input io.Reader, output io.Writer, numTokens int) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lastText, finishReason := "", ""
	generatedTokens := -1
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		if data == "" {
			continue
		}
		var event struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Error json.RawMessage `json:"error"`
			Usage *struct {
				CompletionTokens *int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fmt.Errorf("invalid response from server: %w", err)
		}
		if len(event.Error) > 0 && string(event.Error) != "null" {
			return fmt.Errorf("server error: %s", event.Error)
		}
		if event.Usage != nil && event.Usage.CompletionTokens != nil {
			generatedTokens = *event.Usage.CompletionTokens
		}
		for _, choice := range event.Choices {
			if text := choice.Delta.Content; text != "" {
				if _, err := io.WriteString(output, text); err != nil {
					return err
				}
				lastText = text
			}
			if choice.FinishReason != nil {
				finishReason = *choice.FinishReason
			}
		}
	}
	if lastText != "" && !strings.HasSuffix(lastText, "\n") {
		if _, err := io.WriteString(output, "\n"); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	if finishReason == "" {
		return errors.New("the response stream ended before completion")
	}
	if numTokens > 0 {
		if generatedTokens < 0 {
			return errors.New("the server did not report a token count; could not verify -n")
		}
		if generatedTokens != numTokens {
			return fmt.Errorf("the server generated %d tokens; -n requested %d", generatedTokens, numTokens)
		}
	} else if finishReason == "length" {
		return errors.New("the token limit was reached; the answer is incomplete")
	}
	if lastText == "" {
		if numTokens > 0 {
			return errors.New("the -n token limit was reached before any answer text was produced (thinking tokens count too)")
		}
		return errors.New("the server returned no answer")
	}
	return nil
}
