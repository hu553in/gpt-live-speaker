// Package wakeword hears a wake word with pymicro-wakeword. The binary carries the detector's
// locked Python project and runs it through uv, so the device needs only uv installed.
package wakeword

import (
	"bufio"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
)

//go:embed wakeword.py pyproject.toml uv.lock
var project embed.FS

const (
	cacheDirMode  = 0o750
	cacheFileMode = 0o600
)

// Word is one of the wake words pymicro-wakeword ships pretrained models for.
type Word string

// The pretrained models; any other word needs a custom-trained model.
const (
	HeyJarvis  Word = "hey_jarvis"
	Alexa      Word = "alexa"
	OkayNabu   Word = "okay_nabu"
	HeyMycroft Word = "hey_mycroft"
)

// Words lists every supported wake word.
func Words() []Word {
	return []Word{HeyJarvis, Alexa, OkayNabu, HeyMycroft}
}

// Phrase is the wake word as the user says it.
func (w Word) Phrase() string {
	switch w {
	case HeyJarvis:
		return "hey Jarvis"
	case Alexa:
		return "Alexa"
	case OkayNabu:
		return "okay Nabu"
	case HeyMycroft:
		return "hey Mycroft"
	}
	return string(w)
}

// Detector runs the Python detector as a child process and reports every wake word that crosses
// the threshold.
type Detector struct {
	stdin io.WriteCloser
	hits  chan struct{}
	done  chan error
	// paused stops audio from reaching the detector, so it cannot hear the wake word mid-conversation.
	paused atomic.Bool
}

// Start installs the locked detector environment, which downloads it on the first run, then
// launches the detector. Every wake-word-like sound is logged with its score.
func Start(ctx context.Context, logger *slog.Logger, word Word, threshold float64) (*Detector, error) {
	dir, err := unpack()
	if err != nil {
		return nil, err
	}
	// Install before streaming: audio sent while uv downloads would be lost.
	//nolint:gosec // Fixed command; dir is this package's own cache directory.
	sync := exec.CommandContext(ctx, "uv", "sync", "--directory", dir, "--frozen", "--no-dev", "--quiet")
	if out, syncErr := sync.CombinedOutput(); syncErr != nil {
		return nil, fmt.Errorf("install wake word detector (is uv installed?): %w: %s", syncErr, out)
	}
	//nolint:gosec // Fixed command; the word comes from the validated config, the threshold is a number.
	cmd := exec.CommandContext(ctx, "uv", "run", "--directory", dir, "--no-sync",
		// -u keeps stdout unbuffered, otherwise detections arrive kilobytes late through the pipe.
		"python", "-u", "wakeword.py", string(word), strconv.FormatFloat(threshold, 'f', -1, 64))
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("wake word detector stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("wake word detector stdout: %w", err)
	}
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("start wake word detector: %w", err)
	}

	d := &Detector{stdin: stdin, hits: make(chan struct{}, 1), done: make(chan error, 1)}
	go func() {
		lines := bufio.NewScanner(stdout)
		for lines.Scan() {
			kind, score, _ := strings.Cut(lines.Text(), " ")
			switch kind {
			case "hit":
				select {
				case d.hits <- struct{}{}:
				default:
				}
			case "score":
				logger.InfoContext(ctx, "wake word score", "score", score, "threshold", threshold)
			}
		}
		exitErr := errors.Join(lines.Err(), cmd.Wait())
		if exitErr == nil {
			exitErr = io.EOF
		}
		d.done <- fmt.Errorf("wake word detector exited: %w", exitErr)
	}()
	return d, nil
}

// Hits delivers a value for each detection; detections that arrive while one is pending merge.
func (d *Detector) Hits() <-chan struct{} {
	return d.hits
}

// Done reports why the detector process exited.
func (d *Detector) Done() <-chan error {
	return d.done
}

// Pause stops feeding the detector, for example while a conversation runs.
func (d *Detector) Pause() {
	d.paused.Store(true)
}

// Resume drops detections that arrived while paused and feeds the detector again. The script
// reports one hit per utterance, so the wake word that started the pause cannot fire twice.
func (d *Detector) Resume() {
	select {
	case <-d.hits:
	default:
	}
	d.paused.Store(false)
}

// Feed streams 16 kHz microphone audio to the detector until the process stops reading. Audio
// captured while paused is dropped.
func (d *Detector) Feed(mic <-chan []byte) {
	for chunk := range mic {
		if d.paused.Load() {
			continue
		}
		if _, err := d.stdin.Write(chunk); err != nil {
			// The process is gone; Done reports why. Keep draining so the capture queue never fills.
			for range mic {
			}
			return
		}
	}
}

// unpack writes the embedded Python project to the user cache, where uv keeps its environment.
func unpack() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find the cache directory for the wake word detector: %w", err)
	}
	dir := filepath.Join(cache, "gpt-live-speaker", "wakeword")
	if err = os.MkdirAll(dir, cacheDirMode); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	files, err := project.ReadDir(".")
	if err != nil {
		return "", fmt.Errorf("read the embedded wake word detector: %w", err)
	}
	for _, file := range files {
		data, readErr := project.ReadFile(file.Name())
		if readErr != nil {
			return "", fmt.Errorf("read the embedded %s: %w", file.Name(), readErr)
		}
		if err = os.WriteFile(filepath.Join(dir, file.Name()), data, cacheFileMode); err != nil {
			return "", fmt.Errorf("write %s: %w", file.Name(), err)
		}
	}
	return dir, nil
}
