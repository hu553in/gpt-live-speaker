package wakeword

import (
	"bytes"
	"testing"
)

type recorder struct{ bytes.Buffer }

func (*recorder) Close() error { return nil }

func feed(d *Detector, chunks ...string) {
	mic := make(chan []byte, len(chunks))
	for _, chunk := range chunks {
		mic <- []byte(chunk)
	}
	close(mic)
	d.Feed(mic)
}

func TestPausedDetectorHearsNothingAndResumesClean(t *testing.T) {
	heard := &recorder{}
	d := &Detector{stdin: heard, hits: make(chan struct{}, 1)}

	d.Pause()
	feed(d, "conversation")
	d.hits <- struct{}{} // a detection that arrives while the conversation runs
	d.Resume()
	feed(d, "after")

	if got := heard.String(); got != "after" {
		t.Fatalf("detector heard %q, want only the audio after Resume", got)
	}
	select {
	case <-d.Hits():
		t.Fatal("a detection from before Resume survived")
	default:
	}
}
