package audio

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"time"
)

const (
	// A real room is never digitally silent; seconds of exact zeros mean a muted or blocked microphone.
	mutedAfter = 3 * time.Second
	// Amplitude ratios become decibels as 20·log10.
	decibelsPerDecade = 20
)

// Watch makes a deaf microphone visible: it warns when audio stops arriving or arrives as exact
// digital silence, and reports the level every second at debug log level.
type Watch struct {
	lastChunk   time.Time
	silentSince time.Time
	silent      bool
	missing     bool
	chunks      int
	peak        int
}

// NewWatch starts watching at now, so a microphone that never delivers anything is noticed.
func NewWatch(now time.Time) *Watch {
	return &Watch{lastChunk: now}
}

// Observe records one captured chunk.
func (w *Watch) Observe(ctx context.Context, logger *slog.Logger, chunk []byte, now time.Time) {
	w.lastChunk = now
	w.chunks++
	if w.missing {
		w.missing = false
		logger.InfoContext(ctx, "microphone delivers audio again")
	}
	loudest := 0
	for i := 0; i+1 < len(chunk); i += BytesPerSample {
		//nolint:gosec // Reinterprets the PCM16 bits as a signed sample.
		sample := int(int16(binary.LittleEndian.Uint16(chunk[i:])))
		loudest = max(loudest, sample, -sample)
	}
	w.peak = max(w.peak, loudest)
	if loudest > 0 {
		if w.silent {
			logger.InfoContext(ctx, "microphone delivers sound again")
		}
		w.silent, w.silentSince = false, time.Time{}
		return
	}
	if w.silentSince.IsZero() {
		w.silentSince = now
	}
	if !w.silent && now.Sub(w.silentSince) > mutedAfter {
		w.silent = true
		logger.WarnContext(ctx, "microphone delivers pure silence: check the speakerphone's mute button "+
			"and that this terminal may use the microphone in System Settings > Privacy & Security > Microphone")
	}
}

// Tick runs once a second while listening.
func (w *Watch) Tick(ctx context.Context, logger *slog.Logger, now time.Time, dropped int64) {
	logger.DebugContext(ctx, "microphone",
		"chunks_per_second", w.chunks,
		"peak_dbfs", fmt.Sprintf("%.1f", decibelsPerDecade*math.Log10(float64(w.peak)/math.MaxInt16)),
		"dropped_chunks_total", dropped,
	)
	w.chunks, w.peak = 0, 0
	if !w.missing && now.Sub(w.lastChunk) > mutedAfter {
		w.missing = true
		logger.WarnContext(
			ctx,
			"microphone delivers no audio at all: check AUDIO_DEVICE and the speakerphone's USB cable",
		)
	}
}
