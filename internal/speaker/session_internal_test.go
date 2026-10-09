package speaker

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"gpt-live-speaker/internal/audio"
)

// fakeLive plays the server side of one GPT-Live session through script.
func fakeLive(t *testing.T, script func(ctx context.Context, conn *websocket.Conn) error) (*speaker, <-chan error) {
	t.Helper()
	serverErr := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			serverErr <- errors.New("unexpected Authorization header: " + got)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			serverErr <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		conn.SetReadLimit(maxMessageBytes)
		serverErr <- script(r.Context(), conn)
	}))
	t.Cleanup(srv.Close)
	s := &speaker{
		logger:  slog.New(slog.DiscardHandler),
		key:     "test-key",
		url:     "ws" + strings.TrimPrefix(srv.URL, "http"),
		out:     &audio.Player{},
		dropped: &atomic.Int64{},
		// The defaults from config; the tests close sessions long before these fire.
		backendModel: "gpt-6-luna",
		wakePhrase:   "hey Jarvis",
		idleTimeout:  time.Minute,
		maxSession:   10 * time.Minute,
		history:      history{ttl: time.Hour},
	}
	return s, serverErr
}

func expect(ctx context.Context, conn *websocket.Conn, eventType string) (map[string]any, error) {
	for {
		var ev map[string]any
		if err := wsjson.Read(ctx, conn, &ev); err != nil {
			return nil, err
		}
		if ev["type"] == eventType {
			return ev, nil
		}
	}
}

// startStreamAndClose checks the startup config, starts the session, sends one answer,
// expects the pre-roll, and confirms the client's graceful close.
func startStreamAndClose(audioOut []byte) func(ctx context.Context, conn *websocket.Conn) error {
	return func(ctx context.Context, conn *websocket.Conn) error {
		start, err := expect(ctx, conn, "session.start")
		if err != nil {
			return err
		}
		session, _ := start["session"].(map[string]any)
		audioCfg, _ := session["audio"].(map[string]any)
		format, _ := audioCfg["format"].(map[string]any)
		if session["model"] != "gpt-live-1" || format["type"] != "audio/pcm" ||
			format["rate"] != float64(audio.LiveRate) {
			return errors.New("unexpected session.start config")
		}
		if instructions, _ := session["instructions"].(string); !strings.Contains(instructions, "«hey Jarvis»") {
			return errors.New("session.start does not explain the wake phrase")
		}
		for _, ev := range []map[string]any{
			{"type": "session.started"},
			{"type": "session.output_audio.delta", "delta": base64.StdEncoding.EncodeToString(audioOut)},
			{"type": "session.output_transcript.delta", "delta": "Привет"},
		} {
			if err = wsjson.Write(ctx, conn, ev); err != nil {
				return err
			}
		}
		// The pre-roll must arrive after session.started, upsampled to 24 kHz.
		appended, err := expect(ctx, conn, "session.input_audio.append")
		if err != nil {
			return err
		}
		if pcm, _ := base64.StdEncoding.DecodeString(appended["audio"].(string)); len(pcm) == 0 {
			return errors.New("pre-roll audio was empty")
		}
		if _, err = expect(ctx, conn, "session.close"); err != nil {
			return err
		}
		return wsjson.Write(ctx, conn, map[string]any{
			"type": "session.closed", "reason": "close_requested", "usage": map[string]any{"seconds": 7},
		})
	}
}

func TestConverseStartsStreamsAndClosesGracefullyOnStop(t *testing.T) {
	audioOut := []byte{1, 2, 3, 4}
	s, serverErr := fakeLive(t, startStreamAndClose(audioOut))

	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.converse(ctx, make([]byte, audio.CaptureRate/10*audio.BytesPerSample)) }()

	deadline := time.After(5 * time.Second)
	for s.out.Pending() < len(audioOut) {
		select {
		case <-deadline:
			t.Fatal("output audio never reached the player")
		case <-time.After(10 * time.Millisecond):
		}
	}
	stop()

	if err := <-done; err != nil {
		t.Fatalf("converse: %v", err)
	}
	s.finalizing.Wait()
	if err := <-serverErr; err != nil {
		t.Fatalf("server: %v", err)
	}
	if got := s.history.startInput(time.Now()); len(got) != 1 || got[0].Content[0].Text != "Привет" {
		t.Fatalf("history = %+v, want the assistant's reply", got)
	}
}

func TestConverseEndsWhenTheServerClosesTheSession(t *testing.T) {
	s, serverErr := fakeLive(t, func(ctx context.Context, conn *websocket.Conn) error {
		if _, err := expect(ctx, conn, "session.start"); err != nil {
			return err
		}
		return wsjson.Write(ctx, conn, map[string]any{"type": "session.closed", "reason": "expired"})
	})

	if err := s.converse(t.Context(), nil); err != nil {
		t.Fatalf("converse: %v", err)
	}
	s.finalizing.Wait()
	if err := <-serverErr; err != nil {
		t.Fatalf("server: %v", err)
	}
}

func TestConverseReportsALostConnection(t *testing.T) {
	s, _ := fakeLive(t, func(ctx context.Context, conn *websocket.Conn) error {
		_, err := expect(ctx, conn, "session.start")
		return err
	})

	if err := s.converse(t.Context(), nil); err == nil {
		t.Fatal("converse returned nil after the connection dropped without session.closed")
	}
}

func TestConverseHangsUpWhenTheBackendEndsTheConversation(t *testing.T) {
	s, serverErr := fakeLive(t, func(ctx context.Context, conn *websocket.Conn) error {
		start, err := expect(ctx, conn, "session.start")
		if err != nil {
			return err
		}
		if !strings.Contains(fmt.Sprint(start["session"]), endConversation) {
			return errors.New("session.start does not offer the end_conversation tool")
		}
		for _, ev := range []map[string]any{
			{"type": "session.started"},
			{"type": "response.event", "event": map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{"type": "function_call", "name": endConversation, "call_id": "call_1"},
			}},
		} {
			if err = wsjson.Write(ctx, conn, ev); err != nil {
				return err
			}
		}
		closeEvent, err := expect(ctx, conn, "session.close")
		if err != nil || closeEvent == nil {
			return err
		}
		return wsjson.Write(ctx, conn, map[string]any{"type": "session.closed", "reason": "close_requested"})
	})

	// Neither silence nor Ctrl+C may close the session here: only the goodbye.
	s.idleTimeout, s.maxSession = time.Hour, time.Hour
	done := make(chan error, 1)
	go func() { done <- s.converse(t.Context(), nil) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("converse: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session stayed open after end_conversation")
	}
	s.finalizing.Wait()
	if err := <-serverErr; err != nil {
		t.Fatalf("server: %v", err)
	}
}

func TestSessionConfigTellsBothModelsTheTimeAndPlace(t *testing.T) {
	s := &speaker{backendModel: "gpt-6-luna", wakePhrase: "hey Jarvis", location: "Омск"}
	now := time.Date(2026, 10, 9, 14, 5, 0, 0, time.FixedZone("", 6*60*60))

	cfg := s.sessionConfig(now)

	backend, _ := cfg["delegation"].(map[string]any)["responses"].(map[string]any)
	for name, instructions := range map[string]any{"voice": cfg["instructions"], "backend": backend["instructions"]} {
		text, _ := instructions.(string)
		for _, want := range []string{"2026-10-09 14:05, пятница, часовой пояс UTC+06:00", "Омск"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s instructions lack %q", name, want)
			}
		}
	}
}

func TestMicIsCutOffAfterTheGoodbye(t *testing.T) {
	// conn is nil: any attempt to send the microphone to the model would panic.
	c := &conversation{speaker: &speaker{}, started: true, goodbye: true}

	if err := c.onMic(t.Context(), make([]byte, audio.CaptureRate/10*audio.BytesPerSample)); err != nil {
		t.Fatalf("onMic after the goodbye: %v", err)
	}
}

func TestRejectedStartFailsFastAndForgetsTheHistory(t *testing.T) {
	s, _ := fakeLive(t, func(ctx context.Context, conn *websocket.Conn) error {
		if _, err := expect(ctx, conn, "session.start"); err != nil {
			return err
		}
		err := wsjson.Write(ctx, conn, map[string]any{
			"type": "error", "error": map[string]any{"message": "input exceeds the limit"},
		})
		if err != nil {
			return err
		}
		_, err = expect(ctx, conn, "never sent")
		return err
	})
	s.history.add(roleUser, "a history OpenAI refuses", time.Now())

	started := time.Now()
	err := s.converse(t.Context(), nil)

	if !errors.Is(err, errSessionRejected) || !strings.Contains(err.Error(), "input exceeds the limit") {
		t.Fatalf("converse error = %v, want the rejection with its message", err)
	}
	if waited := time.Since(started); waited > startTimeout/2 {
		t.Fatalf("converse waited %v for a session OpenAI had already rejected", waited)
	}
	if got := s.history.startInput(time.Now()); len(got) != 0 {
		t.Fatalf("history after a rejected start = %+v, want it forgotten", got)
	}
}

func TestHistoryFollowsTheTimelineNotTheArrivalOrder(t *testing.T) {
	s, serverErr := fakeLive(t, func(ctx context.Context, conn *websocket.Conn) error {
		if _, err := expect(ctx, conn, "session.start"); err != nil {
			return err
		}
		for _, ev := range []map[string]any{
			{"type": "session.started"},
			// The reply is transcribed first, although the user spoke before it.
			{"type": "session.output_transcript.delta", "delta": "Пока!", "start_ms": 2000},
			{"type": "session.input_transcript.delta", "delta": "пока", "start_ms": 1500},
			{"type": "session.closed", "reason": "remote_hangup"},
		} {
			if err := wsjson.Write(ctx, conn, ev); err != nil {
				return err
			}
		}
		return nil
	})

	if err := s.converse(t.Context(), nil); err != nil {
		t.Fatalf("converse: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server: %v", err)
	}

	got := s.history.startInput(time.Now())
	if len(got) != 2 || got[0].Role != roleUser || got[1].Role != roleAssistant {
		t.Fatalf("history = %+v, want the user's words before the reply", got)
	}
}
