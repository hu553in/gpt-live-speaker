package speaker

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"gpt-live-speaker/internal/audio"
)

const (
	liveURL = "wss://api.openai.com/v1/live/sessions"
	// session.started echoes the whole configuration; the library's 32 KiB default is too small.
	maxMessageBytes = 4 << 20

	startTimeout = 15 * time.Second
	closeTimeout = 15 * time.Second
	tickInterval = time.Second

	pricePerMinute = 0.05

	// After end_conversation the goodbye still has to play out; the session closes once the
	// speaker has been quiet this long, and no later than goodbyeLimit even if the model talks on.
	goodbyeQuiet = time.Second
	goodbyeLimit = 5 * time.Second
)

// liveEvent holds the fields the speaker reads from GPT-Live server events.
type liveEvent struct {
	Type   string `json:"type"`
	Delta  string `json:"delta"`
	Reason string `json:"reason"`
	Usage  *struct {
		Seconds float64 `json:"seconds"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	// Event is the backend event nested in a response.event envelope.
	Event *struct {
		Type string `json:"type"`
		Item *struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"item"`
	} `json:"event"`
}

// endsConversation reports whether the backend called end_conversation.
func (ev liveEvent) endsConversation() bool {
	return ev.Event != nil && ev.Event.Type == "response.output_item.done" && ev.Event.Item != nil &&
		ev.Event.Item.Type == "function_call" && ev.Event.Item.Name == endConversation
}

// clientEvent covers the commands the speaker sends; Audio is set only for audio appends.
type clientEvent struct {
	Type    string         `json:"type"`
	Session map[string]any `json:"session,omitempty"`
	Audio   string         `json:"audio,omitempty"`
}

type audioFormat struct {
	Type string `json:"type"`
	Rate int    `json:"rate"`
}

// conversation is one GPT-Live session, from the wake word until the session closes.
type conversation struct {
	speaker *speaker
	conn    *websocket.Conn
	up      audio.Upsampler
	// pending holds microphone audio captured while the session was still starting.
	pending      [][]byte
	line         message
	started      bool
	closing      bool
	closed       bool
	connectedAt  time.Time
	lastActivity time.Time
	lastOutput   time.Time
	goodbyeAt    time.Time
	closingAt    time.Time
	seconds      float64
}

// converse runs one session. Audio captured before session.started, including the pre-roll
// with the wake word, is sent once the session is ready, so a request said in one breath survives.
func (s *speaker) converse(ctx context.Context, preroll []byte) error {
	//nolint:bodyclose // coder/websocket handles the handshake response body; callers never close it.
	conn, _, err := websocket.Dial(ctx, s.url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + s.key}},
	})
	if err != nil {
		return fmt.Errorf("connect to GPT-Live: %w", err)
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(maxMessageBytes)

	// Ctrl+C cancels ctx, but session.close must still reach OpenAI, or the session keeps billing.
	sessionCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()

	now := time.Now()
	c := &conversation{speaker: s, conn: conn, connectedAt: now, lastActivity: now}
	defer c.finish(sessionCtx)
	c.pending = append(c.pending, c.up.Process(preroll))
	if err = c.send(sessionCtx, c.startEvent(now)); err != nil {
		return err
	}

	events := make(chan liveEvent)
	readErr := make(chan error, 1)
	go readEvents(sessionCtx, conn, events, readErr)

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	stop := ctx.Done()
	for !c.closed {
		select {
		case <-stop:
			stop = nil
			err = c.onStop(sessionCtx)
		case chunk := <-s.liveMic:
			err = c.onMic(sessionCtx, chunk)
		case ev := <-events:
			err = c.onEvent(sessionCtx, ev)
		case err = <-readErr:
			err = fmt.Errorf("GPT-Live connection lost before session.closed: %w", err)
		case tick := <-ticker.C:
			err = c.onTick(sessionCtx, tick)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *conversation) startEvent(now time.Time) clientEvent {
	session := c.speaker.sessionConfig(now)
	session["audio"] = map[string]any{"format": audioFormat{Type: "audio/pcm", Rate: audio.LiveRate}}
	if input := c.speaker.history.startInput(now); len(input) > 0 {
		session["input"] = input
	}
	return clientEvent{Type: "session.start", Session: session}
}

// onStop handles Ctrl+C: a started session closes gracefully, a starting one is dropped.
func (c *conversation) onStop(ctx context.Context) error {
	if !c.started {
		return context.Canceled
	}
	return c.requestClose(ctx, "stopped")
}

func (c *conversation) onMic(ctx context.Context, chunk []byte) error {
	pcm := c.up.Process(chunk)
	switch {
	case c.closing:
		return nil
	case !c.started:
		c.pending = append(c.pending, pcm)
		return nil
	}
	return c.sendAudio(ctx, pcm)
}

func (c *conversation) onEvent(ctx context.Context, ev liveEvent) error {
	logger := c.speaker.logger
	now := time.Now()
	switch ev.Type {
	case "session.started":
		c.started = true
		logger.InfoContext(ctx, "session started, speak")
		for _, pcm := range c.pending {
			if err := c.sendAudio(ctx, pcm); err != nil {
				return err
			}
		}
		c.pending = nil
	case "session.output_audio.delta":
		pcm, err := base64.StdEncoding.DecodeString(ev.Delta)
		if err != nil {
			return fmt.Errorf("decode output audio: %w", err)
		}
		c.speaker.out.Write(pcm)
		c.lastActivity, c.lastOutput = now, now
	case "session.input_transcript.delta":
		c.transcript(ctx, roleUser, ev.Delta, now)
	case "session.output_transcript.delta":
		c.transcript(ctx, roleAssistant, ev.Delta, now)
	case "response.event", "session.delegation.created":
		// Backend work counts as activity even while nobody speaks.
		c.lastActivity = now
		if ev.endsConversation() && c.goodbyeAt.IsZero() {
			c.goodbyeAt = now
			logger.InfoContext(ctx, "the user said goodbye")
		}
	case "session.usage.updated":
		c.recordUsage(ev)
	case "session.closed":
		c.recordUsage(ev)
		c.closed = true
		logger.InfoContext(ctx, "session closed", "reason", ev.Reason)
	case "error":
		if ev.Error != nil {
			logger.WarnContext(ctx, "GPT-Live error", "message", ev.Error.Message)
		}
	}
	return nil
}

func (c *conversation) onTick(ctx context.Context, now time.Time) error {
	switch {
	case c.closing && now.Sub(c.closingAt) > closeTimeout:
		return errors.New("GPT-Live did not confirm the close; final usage is unknown")
	case !c.started && now.Sub(c.connectedAt) > startTimeout:
		return errors.New("GPT-Live did not start the session")
	case !c.goodbyeAt.IsZero() && (now.Sub(c.goodbyeAt) > goodbyeLimit ||
		c.speaker.out.Pending() == 0 && now.Sub(c.lastOutput) > goodbyeQuiet):
		return c.requestClose(ctx, "goodbye")
	case c.started && now.Sub(c.connectedAt) > c.speaker.maxSession:
		return c.requestClose(ctx, "maximum session length")
	case c.started && now.Sub(c.lastActivity) > c.speaker.idleTimeout && c.speaker.out.Pending() == 0:
		return c.requestClose(ctx, "silence")
	}
	return nil
}

// requestClose sends session.close once and keeps the connection open for session.closed.
func (c *conversation) requestClose(ctx context.Context, why string) error {
	if c.closing {
		return nil
	}
	c.closing = true
	c.closingAt = time.Now()
	c.speaker.logger.InfoContext(ctx, "closing the session", "why", why)
	return c.send(ctx, clientEvent{Type: "session.close"})
}

func (c *conversation) transcript(ctx context.Context, role, delta string, now time.Time) {
	c.speaker.history.add(role, delta, now)
	c.lastActivity = now
	if c.line.role != role {
		c.logLine(ctx)
		c.line.role = role
	}
	c.line.text += delta
}

// logLine prints one speaker's finished turn; fragments arrive in pieces and without turn events.
func (c *conversation) logLine(ctx context.Context) {
	if text := strings.TrimSpace(c.line.text); text != "" {
		who := "you"
		if c.line.role == roleAssistant {
			who = "assistant"
		}
		c.speaker.logger.InfoContext(ctx, who, "text", text)
	}
	c.line = message{}
}

func (c *conversation) recordUsage(ev liveEvent) {
	// usage.seconds is a running total, not an increment.
	if ev.Usage != nil {
		c.seconds = ev.Usage.Seconds
	}
}

// finish logs what the session cost and how the audio path behaved.
func (c *conversation) finish(ctx context.Context) {
	c.logLine(ctx)
	c.speaker.logger.InfoContext(ctx, "session usage",
		"voice_seconds", c.seconds,
		"voice_cost_usd", c.seconds/time.Minute.Seconds()*pricePerMinute,
		// Output arriving faster than real time piles up here and delays interruptions.
		"max_playback_queue", c.speaker.out.TakePeak(),
		"dropped_mic_chunks", c.speaker.dropped.Swap(0),
	)
}

func (c *conversation) sendAudio(ctx context.Context, pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	return c.send(ctx, clientEvent{Type: "session.input_audio.append", Audio: base64.StdEncoding.EncodeToString(pcm)})
}

func (c *conversation) send(ctx context.Context, event clientEvent) error {
	if err := wsjson.Write(ctx, c.conn, event); err != nil {
		return fmt.Errorf("send to GPT-Live: %w", err)
	}
	return nil
}

func readEvents(ctx context.Context, conn *websocket.Conn, events chan<- liveEvent, readErr chan<- error) {
	for {
		var ev liveEvent
		if err := wsjson.Read(ctx, conn, &ev); err != nil {
			readErr <- err
			return
		}
		select {
		case events <- ev:
		case <-ctx.Done():
			return
		}
	}
}
