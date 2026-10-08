// Package speaker runs the conversation loop: wait for the wake word, talk through a GPT-Live
// session, close it when the conversation goes quiet, and listen again.
package speaker

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"gpt-live-speaker/internal/audio"
	"gpt-live-speaker/internal/config"
	"gpt-live-speaker/internal/wakeword"
)

const (
	// The detector prints several hits per utterance, and the room can echo the last answer.
	wakeCooldown = 2 * time.Second
	// A request said in the same breath as the wake word starts before detection finishes.
	prerollBytes = audio.CaptureRate * audio.BytesPerSample
)

type speaker struct {
	logger       *slog.Logger
	key          string
	url          string
	backendModel string
	location     string
	wakePhrase   string
	idleTimeout  time.Duration
	maxSession   time.Duration
	out          *audio.Player
	liveMic      <-chan []byte
	dropped      *atomic.Int64
	history      history
	// quietUntil ignores wake word hits right after a session.
	quietUntil time.Time
}

// Run listens for the wake word and holds conversations until ctx is cancelled.
func Run(ctx context.Context, logger *slog.Logger, cfg config.Config) error {
	logger.InfoContext(ctx, "preparing the wake word detector; the first run downloads it")
	det, err := wakeword.Start(ctx, logger, cfg.WakeWord, cfg.WakeThreshold)
	if err != nil {
		return err
	}
	wakeMic := make(chan []byte, audio.MicQueueChunks)
	liveMic := make(chan []byte, audio.MicQueueChunks)
	s := &speaker{
		logger:       logger,
		key:          cfg.OpenAIAPIKey,
		url:          liveURL,
		backendModel: cfg.BackendModel,
		location:     cfg.Location,
		wakePhrase:   cfg.WakeWord.Phrase(),
		idleTimeout:  cfg.IdleTimeout,
		maxSession:   cfg.MaxSession,
		out:          &audio.Player{},
		liveMic:      liveMic,
		dropped:      &atomic.Int64{},
		history:      history{ttl: cfg.HistoryTTL},
	}
	dev, err := audio.Open(cfg.AudioDevice, wakeMic, liveMic, s.out, s.dropped)
	if err != nil {
		return err
	}
	defer dev.Close()
	go det.Feed(wakeMic)

	for {
		logger.InfoContext(ctx, "listening", "wake_word", s.wakePhrase)
		preroll, waitErr := s.awaitWakeWord(ctx, det)
		if waitErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			return waitErr
		}
		logger.InfoContext(ctx, "wake word heard, connecting")
		s.out.Write(audio.Beep())
		if sessionErr := s.converse(ctx, preroll); sessionErr != nil && ctx.Err() == nil {
			logger.WarnContext(ctx, "session ended with an error", "error", sessionErr)
		}
		if ctx.Err() != nil {
			return nil
		}
		s.quietUntil = time.Now().Add(wakeCooldown)
	}
}

// awaitWakeWord blocks until the wake word and returns the last second of microphone audio.
func (s *speaker) awaitWakeWord(ctx context.Context, det *wakeword.Detector) ([]byte, error) {
	var preroll []byte
	mic := audio.NewWatch(time.Now())
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			mic.Tick(ctx, s.logger, now, s.dropped.Load())
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-det.Done():
			return nil, err
		case chunk := <-s.liveMic:
			mic.Observe(ctx, s.logger, chunk, time.Now())
			preroll = append(preroll, chunk...)
			if extra := len(preroll) - prerollBytes; extra > 0 {
				preroll = preroll[extra:]
			}
		case <-det.Hits():
			if time.Now().After(s.quietUntil) {
				return preroll, nil
			}
		}
	}
}
