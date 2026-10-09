package speaker

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
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
	// tickInterval is how often the session checks its timers and whether the goodbye has played.
	tickInterval = 250 * time.Millisecond

	pricePerMinute = 0.05
)

// errSessionRejected means OpenAI answered session.start with an error instead of session.started.
var errSessionRejected = errors.New("GPT-Live rejected the session")

// liveEvent holds the fields the speaker reads from GPT-Live server events.
type liveEvent struct {
	Type    string `json:"type"`
	Delta   string `json:"delta"`
	StartMS int64  `json:"start_ms"`
	Reason  string `json:"reason"`
	Usage   *struct {
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

// speech is transcript text from one side, placed on the session timeline. The user's words are
// transcribed later than the reply, so arrival order is not the order things were said.
type speech struct {
	role  string
	text  string
	start time.Duration
}

// conversation is one GPT-Live session, from the wake word until the session closes.
type conversation struct {
	speaker *speaker
	conn    *websocket.Conn
	up      audio.Upsampler
	// pending holds microphone audio captured while the session was still starting.
	pending [][]byte
	// line is the stretch the log prints when the other side starts talking.
	line speech
	// said holds every transcript fragment; it becomes history in timeline order when the talk ends.
	said         []speech
	started      bool
	goodbye      bool
	closing      bool
	closed       bool
	connectedAt  time.Time
	lastActivity time.Time
	seconds      float64
	// Audio path stats, taken when the conversation ends, before the next one can reuse the player.
	playbackPeak time.Duration
	droppedMic   int64
}

// converse runs one session. Audio captured before session.started, including the pre-roll
// with the wake word, is sent once the session is ready, so a request said in one breath survives.
// It returns as soon as the session is asked to close, so the speaker listens again right away;
// waiting for OpenAI to confirm the close continues in the background.
func (s *speaker) converse(ctx context.Context, preroll []byte) error {
	//nolint:bodyclose // coder/websocket handles the handshake response body; callers never close it.
	conn, _, err := websocket.Dial(ctx, s.url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + s.key}},
	})
	if err != nil {
		return fmt.Errorf("connect to GPT-Live: %w", err)
	}
	conn.SetReadLimit(maxMessageBytes)

	// Ctrl+C cancels ctx, but session.close must still reach OpenAI, or the session keeps billing.
	sessionCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	now := time.Now()
	c := &conversation{speaker: s, conn: conn, connectedAt: now, lastActivity: now}
	events := make(chan liveEvent)
	readErr := make(chan error, 1)
	go readEvents(sessionCtx, conn, events, readErr)

	err = c.talk(ctx, sessionCtx, preroll, events, readErr)
	c.endTalk(sessionCtx)
	if errors.Is(err, errSessionRejected) {
		// The history is the only part of session.start that changes between sessions, so a
		// rejected start would repeat until it expired; start the next session fresh instead.
		s.history.forget()
	}
	if err != nil || c.closed {
		c.finish(sessionCtx)
		cancel()
		_ = conn.CloseNow()
		return err
	}
	s.finalizing.Go(func() {
		defer cancel()
		defer func() { _ = conn.CloseNow() }()
		c.awaitClosed(sessionCtx, events, readErr)
		c.finish(sessionCtx)
	})
	return nil
}

// talk carries the conversation until the session is asked to close or ends on its own.
func (c *conversation) talk(
	ctx, sessionCtx context.Context,
	preroll []byte,
	events <-chan liveEvent,
	readErr <-chan error,
) error {
	c.pending = append(c.pending, c.up.Process(preroll))
	if err := c.send(sessionCtx, c.startEvent(c.connectedAt)); err != nil {
		return err
	}
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	stop := ctx.Done()
	for !c.closing && !c.closed {
		var err error
		select {
		case <-stop:
			stop = nil
			err = c.onStop(sessionCtx)
		case chunk := <-c.speaker.liveMic:
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

// awaitClosed waits for OpenAI to confirm the close and reports the final usage. Late audio and
// transcripts are dropped: the speaker may already be in the next conversation.
func (c *conversation) awaitClosed(ctx context.Context, events <-chan liveEvent, readErr <-chan error) {
	logger := c.speaker.logger
	timeout := time.NewTimer(closeTimeout)
	defer timeout.Stop()
	for {
		select {
		case ev := <-events:
			switch ev.Type {
			case "session.usage.updated":
				c.recordUsage(ev)
			case "session.closed":
				c.recordUsage(ev)
				logger.InfoContext(ctx, "session closed", "reason", ev.Reason)
				return
			case "error":
				if ev.Error != nil {
					logger.WarnContext(ctx, "GPT-Live error", "message", ev.Error.Message)
				}
			}
		case err := <-readErr:
			logger.WarnContext(ctx, "GPT-Live connection lost before session.closed; final usage is unknown",
				"error", err)
			return
		case <-timeout.C:
			logger.WarnContext(ctx, "GPT-Live did not confirm the close; final usage is unknown")
			return
		}
	}
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
	case c.goodbye:
		// After the goodbye the model must not hear, and answer, anything more.
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
		c.lastActivity = now
	case "session.input_transcript.delta":
		c.transcript(ctx, roleUser, ev, now)
	case "session.output_transcript.delta":
		c.transcript(ctx, roleAssistant, ev, now)
	case "response.event", "session.delegation.created":
		// Backend work counts as activity even while nobody speaks.
		c.lastActivity = now
		if ev.endsConversation() && !c.goodbye {
			c.goodbye = true
			logger.InfoContext(ctx, "the user said goodbye")
		}
	case "session.usage.updated":
		c.recordUsage(ev)
	case "session.closed":
		c.recordUsage(ev)
		c.closed = true
		logger.InfoContext(ctx, "session closed", "reason", ev.Reason)
	case "error":
		message := "no details"
		if ev.Error != nil {
			message = ev.Error.Message
		}
		if !c.started {
			return fmt.Errorf("%w: %s", errSessionRejected, message)
		}
		logger.WarnContext(ctx, "GPT-Live error", "message", message)
	}
	return nil
}

func (c *conversation) onTick(ctx context.Context, now time.Time) error {
	switch {
	case !c.started && now.Sub(c.connectedAt) > startTimeout:
		return errors.New("GPT-Live did not start the session")
	case c.goodbye && c.speaker.out.Pending() == 0:
		// The goodbye is said before the hang-up is handed off, so it has arrived and now played.
		return c.requestClose(ctx, "goodbye")
	case c.started && now.Sub(c.connectedAt) > c.speaker.maxSession:
		return c.requestClose(ctx, "maximum session length")
	case c.started && now.Sub(c.lastActivity) > c.speaker.idleTimeout && c.speaker.out.Pending() == 0:
		return c.requestClose(ctx, "silence")
	}
	return nil
}

// requestClose sends session.close once; the session stays open until OpenAI confirms it.
func (c *conversation) requestClose(ctx context.Context, why string) error {
	if c.closing {
		return nil
	}
	c.closing = true
	c.speaker.logger.InfoContext(ctx, "closing the session", "why", why)
	return c.send(ctx, clientEvent{Type: "session.close"})
}

func (c *conversation) transcript(ctx context.Context, role string, ev liveEvent, now time.Time) {
	start := time.Duration(ev.StartMS) * time.Millisecond
	c.said = append(c.said, speech{role: role, text: ev.Delta, start: start})
	c.lastActivity = now
	if c.line.role != role {
		c.logLine(ctx)
		c.line = speech{role: role, start: start}
	}
	c.line.text += ev.Delta
}

// logLine prints one speaker's finished stretch; fragments arrive in pieces and without turn events.
func (c *conversation) logLine(ctx context.Context) {
	if text := strings.TrimSpace(c.line.text); text != "" {
		who := "you"
		if c.line.role == roleAssistant {
			who = "assistant"
		}
		c.speaker.logger.InfoContext(ctx, who, "at", c.line.start, "text", text)
	}
	c.line = speech{}
}

func (c *conversation) recordUsage(ev liveEvent) {
	// usage.seconds is a running total, not an increment.
	if ev.Usage != nil {
		c.seconds = ev.Usage.Seconds
	}
}

// endTalk logs the last turn, saves the conversation to history in the order it was said, and
// takes the audio path stats while this conversation still owns them.
func (c *conversation) endTalk(ctx context.Context) {
	c.logLine(ctx)
	// Stable, so fragments with the same start keep their arrival order.
	slices.SortStableFunc(c.said, func(a, b speech) int { return cmp.Compare(a.start, b.start) })
	now := time.Now()
	for _, s := range c.said {
		c.speaker.history.add(s.role, s.text, now)
	}
	c.playbackPeak = c.speaker.out.TakePeak()
	c.droppedMic = c.speaker.dropped.Swap(0)
}

// finish logs what the session cost and how the audio path behaved.
func (c *conversation) finish(ctx context.Context) {
	c.speaker.logger.InfoContext(ctx, "session usage",
		"voice_seconds", c.seconds,
		"voice_cost_usd", c.seconds/time.Minute.Seconds()*pricePerMinute,
		// Output arriving faster than real time piles up here and delays interruptions.
		"max_playback_queue", c.playbackPeak,
		"dropped_mic_chunks", c.droppedMic,
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
