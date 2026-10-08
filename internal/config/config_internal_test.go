package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"gpt-live-speaker/internal/wakeword"
)

func TestLoadAppliesDefaultsForEmptyValues(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	// .env.example ships every variable, mostly empty; empty must mean the default.
	for _, name := range []string{"AUDIO_DEVICE", "WAKE_WORD", "WAKE_THRESHOLD", "IDLE_TIMEOUT", "MAX_SESSION",
		"HISTORY_TTL", "BACKEND_MODEL", "LOCATION", "LOG_LEVEL"} {
		t.Setenv(name, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	want := Config{
		OpenAIAPIKey: "test-key", WakeWord: wakeword.HeyJarvis, WakeThreshold: 0.5, IdleTimeout: time.Minute,
		MaxSession: 10 * time.Minute, HistoryTTL: time.Hour, BackendModel: "gpt-6-luna", LogLevel: slog.LevelInfo,
	}
	if cfg != want {
		t.Fatalf("Load() = %+v, want %+v", cfg, want)
	}
}

func TestLoadRejectsInvalidSettings(t *testing.T) {
	for name, value := range map[string]string{
		"WAKE_WORD":      "hey_siri",
		"WAKE_THRESHOLD": "1.5",
		"IDLE_TIMEOUT":   "0s",
		"MAX_SESSION":    "-1m",
		"HISTORY_TTL":    "-1h",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", "test-key")
			t.Setenv(name, value)

			_, err := Load()

			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("Load() error = %v, want one naming %s", err, name)
			}
		})
	}
}

func TestLoadRequiresTheAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("Load() error = %v, want one naming OPENAI_API_KEY", err)
	}
}
