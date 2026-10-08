package wakeword

import (
	"bytes"
	"encoding/binary"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// speech renders text to 16 kHz PCM16 with macOS text-to-speech.
func speech(t *testing.T, text string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "speech.wav")
	//nolint:gosec // Test-only call with a temp path and a fixed phrase.
	say := exec.CommandContext(t.Context(), "say", "-o", path, "--data-format=LEI16@16000", text)
	if out, err := say.CombinedOutput(); err != nil {
		t.Fatalf("say: %v: %s", err, out)
	}
	wav, err := os.ReadFile(path) //nolint:gosec // Reads the temp file the test just wrote.
	if err != nil {
		t.Fatal(err)
	}
	// Skip the RIFF chunks before the samples; say adds more than the canonical 44-byte header.
	for i := 12; i+8 <= len(wav); {
		size := int(binary.LittleEndian.Uint32(wav[i+4:]))
		if string(wav[i:i+4]) == "data" {
			return wav[i+8 : min(i+8+size, len(wav))]
		}
		i += 8 + size
	}
	t.Fatal("no data chunk in the WAV file")
	return nil
}

func TestDetectorHearsHeyJarvis(t *testing.T) {
	for _, tool := range []string{"say", "uv"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not available", tool)
		}
	}
	det, err := Start(t.Context(), slog.New(slog.DiscardHandler), HeyJarvis, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	mic := make(chan []byte, 1)
	go det.Feed(mic)

	silence := make([]byte, 16000*2) // a second of 16 kHz PCM16
	mic <- bytes.Join([][]byte{speech(t, "Hey Jarvis. What's the weather like?"), silence}, nil)

	select {
	case <-det.Hits():
	case exitErr := <-det.Done():
		t.Fatal(exitErr)
	case <-time.After(2 * time.Minute): // The first run downloads the detector.
		t.Fatal("no detection for a spoken hey jarvis")
	}
}
