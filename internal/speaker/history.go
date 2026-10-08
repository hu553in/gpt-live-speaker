package speaker

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// GPT-Live accepts up to 128 startup messages and 8,192 tokens; the character cap keeps
	// Russian text, which takes more tokens per character than English, safely below that.
	historyMaxMessages = 128
	historyMaxChars    = 12000

	roleUser      = "user"
	roleAssistant = "assistant"
)

type message struct {
	role string
	text string
}

// inputMessage is one startup history item in GPT-Live's message format.
type inputMessage struct {
	Type    string      `json:"type"`
	Role    string      `json:"role"`
	Content []inputPart `json:"content"`
}

type inputPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// history keeps recent transcripts so the next session continues the conversation.
type history struct {
	// ttl forgets the conversation after a pause; the old talk confuses a fresh request.
	ttl      time.Duration
	messages []message
	updated  time.Time
}

// add appends a transcript fragment, starting a new message when the speaker changes.
func (h *history) add(role, delta string, now time.Time) {
	h.updated = now
	if n := len(h.messages); n > 0 && h.messages[n-1].role == role {
		h.messages[n-1].text += delta
		return
	}
	h.messages = append(h.messages, message{role: role, text: delta})
	if len(h.messages) > historyMaxMessages {
		h.messages = h.messages[1:]
	}
}

// startInput returns the recent history as GPT-Live startup messages, forgetting it once stale.
func (h *history) startInput(now time.Time) []inputMessage {
	if now.Sub(h.updated) > h.ttl {
		h.messages = nil
	}
	var input []inputMessage
	chars := 0
	for _, m := range slices.Backward(h.messages) {
		text := strings.TrimSpace(m.text)
		if text == "" {
			continue
		}
		chars += utf8.RuneCountInString(text)
		if chars > historyMaxChars {
			break
		}
		part := "input_text"
		if m.role == roleAssistant {
			part = "output_text"
		}
		input = append(
			input,
			inputMessage{Type: "message", Role: m.role, Content: []inputPart{{Type: part, Text: text}}},
		)
	}
	slices.Reverse(input)
	return input
}
