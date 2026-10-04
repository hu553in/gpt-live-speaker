package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

const voicePrompt = `Ты голосовой ассистент в умной колонке. Говори по-русски, естественно и коротко, как в живом разговоре.

Backchannel policy: Use moderate backchannels. Acknowledge naturally without competing with the main response.

Interruption policy: Stop speaking when the user interrupts. Listen to what they say.

Delegation policy:
Backend tools:
- Reasoning: think through hard questions and multi-step problems.

Delegate to the backend when:
- The question needs careful reasoning or a long, precise answer.

Do not delegate to the backend when:
- You can answer from the conversation directly.`

const backendPrompt = "Отвечай по-русски. Ответ будет произнесён вслух: коротко, без markdown, списков и ссылок."

const (
	addr   = "127.0.0.1:3000"
	origin = "http://localhost:3000"

	readHeaderTimeout = 10 * time.Second
	upstreamTimeout   = 30 * time.Second
)

//go:embed web/dist
var dist embed.FS

func main() {
	static, _ := fs.Sub(dist, "web/dist")
	http.Handle("GET /", http.FileServerFS(static))
	http.HandleFunc("POST /api/session", createSession)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	//nolint:forbidigo // Startup banner for the terminal, printed once the port is bound.
	fmt.Print("\n  ▲ GPT-Live Speaker\n  - Local: " + origin + "\n\n")
	log.Fatal((&http.Server{ReadHeaderTimeout: readHeaderTimeout}).Serve(ln))
}

func createSession(w http.ResponseWriter, r *http.Request) {
	// Stops other websites from opening paid sessions through this localhost server.
	if r.Header.Get("Origin") != origin {
		http.Error(w, "open the page at "+origin, http.StatusForbidden)
		return
	}
	sdp, _ := io.ReadAll(r.Body)
	body, _ := json.Marshal(map[string]any{
		"session": map[string]any{
			"model":        "gpt-live-1",
			"instructions": voicePrompt,
			"delegation": map[string]any{
				"type":      "responses",
				"responses": map[string]any{"model": "gpt-6-luna", "instructions": backendPrompt},
			},
		},
		"transport": map[string]any{"type": "webrtc", "sdp": string(sdp)},
	})
	req, _ := http.NewRequestWithContext(
		r.Context(),
		http.MethodPost,
		"https://api.openai.com/v1/live/sessions",
		bytes.NewReader(body),
	)
	req.Header.Set("Authorization", "Bearer "+os.Getenv("OPENAI_API_KEY"))
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: upstreamTimeout}).Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	w.Header().Set("Content-Type", res.Header.Get("Content-Type"))
	w.WriteHeader(res.StatusCode)
	// The browser has gone if the copy fails; there is no one left to report it to.
	_, _ = io.Copy(w, res.Body)
}
