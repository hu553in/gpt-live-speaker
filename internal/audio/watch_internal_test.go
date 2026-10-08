package audio

import (
	"log/slog"
	"testing"
	"time"
)

func TestWatchWarnsOnlyAfterSustainedDigitalSilence(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	start := time.Now()
	silence, sound := make([]byte, 320), []byte{0, 0, 1, 0}

	w := NewWatch(start)
	w.Observe(t.Context(), logger, silence, start)
	w.Observe(t.Context(), logger, silence, start.Add(mutedAfter/2))
	if w.silent {
		t.Fatal("warned before the silence lasted long enough")
	}
	w.Observe(t.Context(), logger, silence, start.Add(mutedAfter+time.Second))
	if !w.silent {
		t.Fatal("no warning after sustained silence")
	}
	w.Observe(t.Context(), logger, sound, start.Add(mutedAfter+2*time.Second))
	if w.silent || !w.silentSince.IsZero() {
		t.Fatal("sound did not reset the watch")
	}
}

func TestWatchWarnsWhenAudioStopsArriving(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	start := time.Now()
	w := NewWatch(start)

	w.Tick(t.Context(), logger, start.Add(mutedAfter/2), 0)
	if w.missing {
		t.Fatal("warned before audio was missing long enough")
	}
	w.Tick(t.Context(), logger, start.Add(mutedAfter+time.Second), 0)
	if !w.missing {
		t.Fatal("no warning when the microphone stopped delivering audio")
	}
	w.Observe(t.Context(), logger, []byte{0, 1}, start.Add(mutedAfter+2*time.Second))
	if w.missing {
		t.Fatal("new audio did not clear the warning")
	}
}
