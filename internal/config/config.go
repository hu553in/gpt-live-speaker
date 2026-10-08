// Package config reads the speaker's settings from the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/caarlos0/env/v11"

	"gpt-live-speaker/internal/wakeword"
)

// Config holds every setting; README.md documents each variable. WAKE_THRESHOLD defaults to 0.5
// because the pretrained models score a real voice through a speakerphone far below their own 0.97.
type Config struct {
	OpenAIAPIKey  string        `env:"OPENAI_API_KEY,required,notEmpty"`
	AudioDevice   string        `env:"AUDIO_DEVICE"`
	WakeWord      wakeword.Word `env:"WAKE_WORD"                        envDefault:"hey_jarvis"`
	WakeThreshold float64       `env:"WAKE_THRESHOLD"                   envDefault:"0.5"`
	IdleTimeout   time.Duration `env:"IDLE_TIMEOUT"                     envDefault:"1m"`
	MaxSession    time.Duration `env:"MAX_SESSION"                      envDefault:"10m"`
	HistoryTTL    time.Duration `env:"HISTORY_TTL"                      envDefault:"1h"`
	BackendModel  string        `env:"BACKEND_MODEL"                    envDefault:"gpt-6-luna"`
	Location      string        `env:"LOCATION"`
	LogLevel      slog.Level    `env:"LOG_LEVEL"                        envDefault:"info"`
}

// Load reads and validates the configuration.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("%w; copy .env.example to .env and fill it in", err)
	}
	if err = cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	var errs []error
	if !slices.Contains(wakeword.Words(), c.WakeWord) {
		errs = append(errs, fmt.Errorf("WAKE_WORD=%q must be one of %v", c.WakeWord, wakeword.Words()))
	}
	if c.WakeThreshold <= 0 || c.WakeThreshold >= 1 {
		errs = append(errs, fmt.Errorf("WAKE_THRESHOLD=%v must be between 0 and 1, such as 0.5", c.WakeThreshold))
	}
	if c.IdleTimeout <= 0 {
		errs = append(errs, fmt.Errorf("IDLE_TIMEOUT=%v must be positive, such as 1m", c.IdleTimeout))
	}
	if c.MaxSession <= 0 {
		errs = append(errs, fmt.Errorf("MAX_SESSION=%v must be positive, such as 10m", c.MaxSession))
	}
	if c.HistoryTTL < 0 {
		errs = append(
			errs,
			fmt.Errorf("HISTORY_TTL=%v must not be negative; 0 starts every session fresh", c.HistoryTTL),
		)
	}
	return errors.Join(errs...)
}
