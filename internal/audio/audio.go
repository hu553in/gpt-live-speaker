// Package audio drives the speakerphone: microphone capture, a playback queue, and the rate
// conversion between the wake word detector's 16 kHz and GPT-Live's 24 kHz.
package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/gen2brain/malgo"
)

const (
	// CaptureRate is the microphone rate: pymicro-wakeword only accepts 16 kHz mono PCM16.
	CaptureRate = 16000
	// LiveRate is GPT-Live's default PCM rate; playback keeps full speech quality.
	LiveRate = 24000
	// BytesPerSample is the size of one mono PCM16 sample.
	BytesPerSample = 2
	// MicQueueChunks bounds each microphone queue; the capture callback drops chunks beyond it.
	MicQueueChunks = 256

	// Upsampling 16 kHz to 24 kHz emits three output samples for every two input samples.
	upNum = 3
	upDen = 2

	beepHz       = 880
	beepDuration = 150 * time.Millisecond
	beepFade     = 10 * time.Millisecond
	beepVolume   = 0.3
)

// Player is the playback queue; the audio callback drains it and plays silence when it is empty.
type Player struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	peak int
}

// Write queues PCM16 audio at LiveRate.
func (p *Player) Write(pcm []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf.Write(pcm)
	p.peak = max(p.peak, p.buf.Len())
}

// Read fills out from the queue and pads the rest with silence.
func (p *Player) Read(out []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n, _ := p.buf.Read(out)
	clear(out[n:])
}

// Pending is the number of queued bytes not yet played.
func (p *Player) Pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buf.Len()
}

// TakePeak returns the deepest the queue got since the previous call, as playback time.
func (p *Player) TakePeak() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	peak := p.peak
	p.peak = p.buf.Len()
	return time.Duration(peak) * time.Second / (LiveRate * BytesPerSample)
}

// Devices owns the speakerphone's microphone and speaker.
type Devices struct {
	ctx      *malgo.AllocatedContext
	capture  *malgo.Device
	playback *malgo.Device
}

// Open starts both devices: the ones whose names contain device, or the system defaults when
// device is empty. The capture callback never blocks: a full queue drops the chunk.
func Open(device string, wakeMic, liveMic chan<- []byte, out *Player, dropped *atomic.Int64) (*Devices, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("init audio: %w", err)
	}
	a := &Devices{ctx: ctx}

	captureCfg := malgo.DefaultDeviceConfig(malgo.Capture)
	captureCfg.Capture.Format = malgo.FormatS16
	captureCfg.Capture.Channels = 1
	captureCfg.SampleRate = CaptureRate
	if device != "" {
		if captureCfg.Capture.DeviceID, err = findDevice(ctx, malgo.Capture, device); err != nil {
			a.Close()
			return nil, err
		}
	}
	a.capture, err = malgo.InitDevice(ctx.Context, captureCfg, malgo.DeviceCallbacks{
		Data: func(_, in []byte, _ uint32) {
			for _, queue := range []chan<- []byte{wakeMic, liveMic} {
				select {
				case queue <- bytes.Clone(in):
				default:
					dropped.Add(1)
				}
			}
		},
	})
	if err != nil {
		a.Close()
		return nil, fmt.Errorf("open microphone: %w", err)
	}

	playbackCfg := malgo.DefaultDeviceConfig(malgo.Playback)
	playbackCfg.Playback.Format = malgo.FormatS16
	playbackCfg.Playback.Channels = 1
	playbackCfg.SampleRate = LiveRate
	if device != "" {
		if playbackCfg.Playback.DeviceID, err = findDevice(ctx, malgo.Playback, device); err != nil {
			a.Close()
			return nil, err
		}
	}
	a.playback, err = malgo.InitDevice(ctx.Context, playbackCfg, malgo.DeviceCallbacks{
		Data: func(outBuf, _ []byte, _ uint32) { out.Read(outBuf) },
	})
	if err != nil {
		a.Close()
		return nil, fmt.Errorf("open speaker: %w", err)
	}

	for _, dev := range []*malgo.Device{a.capture, a.playback} {
		if err = dev.Start(); err != nil {
			a.Close()
			return nil, fmt.Errorf("start audio device: %w", err)
		}
	}
	return a, nil
}

// findDevice returns the ID of the first device whose name contains name, ignoring case.
func findDevice(ctx *malgo.AllocatedContext, kind malgo.DeviceType, name string) (unsafe.Pointer, error) {
	devices, err := ctx.Devices(kind)
	if err != nil {
		return nil, fmt.Errorf("list audio devices: %w", err)
	}
	names := make([]string, 0, len(devices))
	for i := range devices {
		if strings.Contains(strings.ToLower(devices[i].Name()), strings.ToLower(name)) {
			// miniaudio copies the ID when the device opens; the small C copy lives as long as the process.
			return devices[i].ID.Pointer(), nil
		}
		names = append(names, devices[i].Name())
	}
	return nil, fmt.Errorf("no audio device matches AUDIO_DEVICE=%q; available: %s", name, strings.Join(names, "; "))
}

// Close stops both devices.
func (a *Devices) Close() {
	for _, dev := range []*malgo.Device{a.capture, a.playback} {
		if dev != nil {
			dev.Uninit()
		}
	}
	_ = a.ctx.Uninit()
	a.ctx.Free()
}

// Upsampler converts a 16 kHz PCM16 stream to 24 kHz by linear interpolation, keeping state between chunks.
type Upsampler struct {
	samples []int16
	// pos is the next output position in thirds of an input sample, counted from samples[0].
	pos int
}

// Process converts the next chunk; the output may lag the input by one sample.
func (u *Upsampler) Process(pcm []byte) []byte {
	for i := 0; i+1 < len(pcm); i += BytesPerSample {
		//nolint:gosec // Reinterprets the PCM16 bits as a signed sample.
		u.samples = append(u.samples, int16(binary.LittleEndian.Uint16(pcm[i:])))
	}
	out := make([]byte, 0, len(pcm)*upNum/upDen+BytesPerSample)
	// Interpolation needs the sample after the current position, so the last one waits for the next chunk.
	for u.pos/upNum+1 < len(u.samples) {
		i, frac := u.pos/upNum, u.pos%upNum
		a, b := int(u.samples[i]), int(u.samples[i+1])
		//nolint:gosec // Interpolated between two int16 samples, then stored as PCM16 bits.
		out = binary.LittleEndian.AppendUint16(out, uint16(int16(a+(b-a)*frac/upNum)))
		u.pos += upDen
	}
	consumed := u.pos / upNum
	u.samples = append(u.samples[:0], u.samples[consumed:]...)
	u.pos -= consumed * upNum
	return out
}

// Beep is the short tone that confirms the wake word was heard.
func Beep() []byte {
	total := int(beepDuration.Seconds() * LiveRate)
	fade := int(beepFade.Seconds() * LiveRate)
	out := make([]byte, 0, total*BytesPerSample)
	for i := range total {
		gain := beepVolume * min(1, float64(i)/float64(fade), float64(total-i)/float64(fade))
		v := gain * math.Sin(2*math.Pi*beepHz*float64(i)/LiveRate) * math.MaxInt16
		out = binary.LittleEndian.AppendUint16(out, uint16(int16(v))) //nolint:gosec // |v| stays below MaxInt16.
	}
	return out
}
