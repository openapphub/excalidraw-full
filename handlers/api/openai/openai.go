package openai

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/render"
)

var (
	openaiAPIKey  string
	openaiBaseURL string
)

func Init() {
	openaiAPIKey = os.Getenv("OPENAI_API_KEY")
	openaiBaseURL = os.Getenv("OPENAI_BASE_URL")
	if openaiBaseURL == "" {
		openaiBaseURL = "https://api.openai.com" // Default value
	}
	if openaiAPIKey == "" {
		log.Println("WARNING: OPENAI_API_KEY environment variable not set. OpenAI proxy will not work.")
	}
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
		if openaiAPIKey == "" {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"error": "OpenAI API key is not configured on the server"})
			return
		}

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

		// Create the proxy request to OpenAI.
		// The client may override the target via X-AI-Base-URL (used when the
		// page is served over http and the AI service is https — the browser
		// blocks mixed content, so the frontend routes through this proxy and
		// passes the real target here; the proxy makes the https call
		// server-side).
		targetBase := openaiBaseURL
		if override := r.Header.Get("X-AI-Base-URL"); override != "" {
			targetBase = override
		}
		proxyURL := targetBase + "/chat/completions"
		proxyReq, err := http.NewRequestWithContext(r.Context(), "POST", proxyURL, bytes.NewReader(body))
		if err != nil {
			render.Status(r, http.StatusInternalServerError)
			render.JSON(w, r, map[string]string{"error": "Failed to create proxy request"})
			return
		}

		// Set necessary headers
		proxyReq.Header.Set("Authorization", "Bearer "+openaiAPIKey)
		proxyReq.Header.Set("Content-Type", "application/json")
		proxyReq.Header.Set("Accept", "application/json")

		// Send the request to OpenAI
		client := &http.Client{Timeout: 5 * time.Minute}
		resp, err := client.Do(proxyReq)
		if err != nil {
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
