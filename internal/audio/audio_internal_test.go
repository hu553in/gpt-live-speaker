package audio

import (
	"encoding/binary"
	"math"
	"testing"
)

func pcm16(samples []float64) []byte {
	out := make([]byte, 0, len(samples)*BytesPerSample)
	for _, s := range samples {
		out = binary.LittleEndian.AppendUint16(out, uint16(int16(s))) //nolint:gosec // Test signals fit int16.
	}
	return out
}

func decode(pcm []byte) []int16 {
	out := make([]int16, 0, len(pcm)/BytesPerSample)
	for i := 0; i+1 < len(pcm); i += BytesPerSample {
		out = append(out, int16(binary.LittleEndian.Uint16(pcm[i:]))) //nolint:gosec // Reinterprets PCM16 bits.
	}
	return out
}

func TestUpsamplerMatchesTheContinuousSignal(t *testing.T) {
	const hz, amplitude = 440.0, 10000.0
	in := make([]float64, CaptureRate)
	for i := range in {
		in[i] = amplitude * math.Sin(2*math.Pi*hz*float64(i)/CaptureRate)
	}

	var u Upsampler
	got := decode(u.Process(pcm16(in)))

	if n := len(got); n < LiveRate-upNum || n > LiveRate {
		t.Fatalf("one second at 16 kHz gave %d samples, want about %d", n, LiveRate)
	}
	// Linear interpolation of a 440 Hz tone at 16 kHz stays within ~0.4% of the true wave.
	for i, sample := range got {
		want := amplitude * math.Sin(2*math.Pi*hz*float64(i)/LiveRate)
		if math.Abs(float64(sample)-want) > amplitude/100 {
			t.Fatalf("sample %d = %d, want %.0f", i, sample, want)
		}
	}
}

func TestUpsamplerIgnoresChunkBoundaries(t *testing.T) {
	in := make([]float64, 1000)
	for i := range in {
		in[i] = float64(i*37%2000 - 1000)
	}
	pcm := pcm16(in)

	var whole Upsampler
	want := whole.Process(pcm)

	var chunked Upsampler
	var got []byte
	// Odd chunk sizes exercise the carried sample and fractional position.
	for start, size := 0, 2; start < len(pcm); start, size = start+size, size%14+4 {
		got = append(got, chunked.Process(pcm[start:min(start+size, len(pcm))])...)
	}

	if string(got) != string(want) {
		t.Fatalf("chunked output differs from one-pass output: %d vs %d bytes", len(got), len(want))
	}
}

func TestPlayerPlaysSilenceWhenEmpty(t *testing.T) {
	var p Player
	p.Write([]byte{1, 2})

	out := []byte{9, 9, 9, 9}
	p.Read(out)

	if string(out) != string([]byte{1, 2, 0, 0}) {
		t.Fatalf("read = %v, want queued audio followed by silence", out)
	}
}
