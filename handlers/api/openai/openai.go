package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/render"
)

var (
	openaiAPIKey  string
	openaiBaseURL string
)

var errMissingClientAPIKey = errors.New("client API key is required")

func Init() {
	openaiAPIKey = os.Getenv("OPENAI_API_KEY")
	openaiBaseURL = os.Getenv("OPENAI_BASE_URL")
	if openaiBaseURL == "" {
		openaiBaseURL = "https://api.openai.com/v1"
	}
	if openaiAPIKey == "" {
		log.Println("WARNING: OPENAI_API_KEY environment variable not set. OpenAI proxy requires a client-provided API key.")
	}
}

func buildChatCompletionsURL(baseURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("AI base URL must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("AI base URL must include a host")
	}
	if parsed.User != nil {
		return nil, errors.New("AI base URL must not include user information")
	}
	if parsed.Fragment != "" {
		return nil, errors.New("AI base URL must not include a fragment")
	}

	// 兼容服务商 Base URL 和完整的 chat/completions endpoint，避免重复拼接路径。
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(parsed.Path, "/chat/completions") {
		parsed.RawPath = ""
		return parsed, nil
	}
	if parsed.Path == "" {
		parsed.Path = "/v1"
	}

	joined, err := url.JoinPath(parsed.String(), "chat/completions")
	if err != nil {
		return nil, err
	}
	return url.Parse(joined)
}

func bearerAuthorization(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return "Bearer " + parts[1], true
}

func selectProxyAuthorization(clientHeader, serverAPIKey string, clientTarget bool) (string, error) {
	clientAuthorization, hasClientAuthorization := bearerAuthorization(clientHeader)
	if clientTarget {
		if !hasClientAuthorization {
			return "", errMissingClientAPIKey
		}
		return clientAuthorization, nil
	}

	if serverAPIKey = strings.TrimSpace(serverAPIKey); serverAPIKey != "" {
		return "Bearer " + serverAPIKey, nil
	}
	if hasClientAuthorization {
		return clientAuthorization, nil
	}
	return "", errMissingClientAPIKey
}

// Structures for OpenAI compatibility

type LiteralType string

const (
	LiteralTypeText     LiteralType = "text"
	LiteralTypeImageURL LiteralType = "image_url"
)

// UserTextContentPart corresponds to a part of a multi-part message with text.
type UserTextContentPart struct {
	Type LiteralType `json:"type"`
	Text string      `json:"text"`
}

// ImageURL details the URL and detail level of an image.
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// UserImageContentPart corresponds to a part of a multi-part message with an image.
type UserImageContentPart struct {
	Type     LiteralType `json:"type"`
	ImageURL ImageURL    `json:"image_url"`
}

type UserContentPart struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

type UserContext struct {
	UserID int `json:"user_id"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // Can be string or a slice of UserTextContentPart/UserImageContentPart
	Name    string `json:"name,omitempty"`
}

type ChatCompletionRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	MaxTokens *int          `json:"max_tokens,omitempty"`
	Stream    *bool         `json:"stream"`
	// Other fields like temperature, max_tokens etc. are ignored for this mock
}

type ChatCompletionChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   Usage                  `json:"usage"`
}

// FlusherWriter is a helper to ensure that data is flushed to the client for streaming
type FlusherWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func (fw *FlusherWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if fw.f != nil {
		fw.f.Flush()
	}
	return n, err
}

func HandleChatCompletion() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Read the original request body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"error": "Failed to read request body"})
			return
		}
		defer r.Body.Close()

		// Unmarshal to check if it's a streaming request
		var req ChatCompletionRequest
		if err := json.Unmarshal(body, &req); err != nil {
			render.Status(r, http.StatusBadRequest)
			render.JSON(w, r, map[string]string{"error": "Invalid JSON in request body"})
			return
		}

		// 客户端可显式覆盖目标，用于从 HTTP 页面代理访问 HTTPS AI 服务。
		targetBase := openaiBaseURL
		override := strings.TrimSpace(r.Header.Get("X-AI-Base-URL"))
		clientTarget := override != ""
		if clientTarget {
			targetBase = override
		}
		proxyURL, err := buildChatCompletionsURL(targetBase)
		if err != nil {
			status := http.StatusInternalServerError
			if clientTarget {
				status = http.StatusBadRequest
			}
			render.Status(r, status)
			render.JSON(w, r, map[string]string{"error": "Invalid AI base URL"})
			return
		}

		authHeader, err := selectProxyAuthorization(r.Header.Get("Authorization"), openaiAPIKey, clientTarget)
		if err != nil {
			status := http.StatusInternalServerError
			message := "OpenAI API key is not configured on the server"
			if clientTarget {
				status = http.StatusUnauthorized
				message = "Client API key is required for a client-provided AI base URL"
			}
			render.Status(r, status)
			render.JSON(w, r, map[string]string{"error": message})
			return
		}

		proxyReq, err := http.NewRequestWithContext(r.Context(), "POST", proxyURL.String(), bytes.NewReader(body))
		if err != nil {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"error": "Failed to create proxy request"})
			return
		}

		// 动态目标只使用客户端密钥；服务端密钥只发送到服务端配置的目标。
		proxyReq.Header.Set("Authorization", authHeader)
		proxyReq.Header.Set("Content-Type", "application/json")
		proxyReq.Header.Set("Accept", "application/json")

		// Send the request to OpenAI
		client := &http.Client{
			Timeout: 5 * time.Minute,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("AI provider redirects are not allowed")
			},
		}
		resp, err := client.Do(proxyReq)
		if err != nil {
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
			}
			log.Printf("AI provider request failed target=%s: %v", proxyURL.Redacted(), err)
			render.Status(r, http.StatusBadGateway)
			render.JSON(w, r, map[string]string{"error": "Failed to communicate with OpenAI API"})
			return
		}
		defer resp.Body.Close()

		// Handle the response based on whether it's a stream or not
		if req.Stream != nil && *req.Stream {
			// Streaming response
			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
				return
			}

			// Copy headers from OpenAI response to our response
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(resp.StatusCode)

			fw := &FlusherWriter{w: w, f: flusher}
			if _, err := io.Copy(fw, resp.Body); err != nil {
				// Log error, but the response is likely already sent/broken.
				log.Printf("Error streaming response from OpenAI: %v", err)
			}
		} else {
			// Non-streaming response
			// Copy headers from OpenAI response
			for key, values := range resp.Header {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
		}
	}
}
