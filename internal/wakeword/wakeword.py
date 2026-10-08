"""Hear a wake word in 16 kHz mono PCM16 from stdin for the speaker daemon.

Usage: wakeword.py <model> <threshold>, where model is a pymicro-wakeword builtin such as
hey_jarvis. Prints "hit <score>" as soon as a sound crosses the threshold, and "score <score>"
once every wake-word-like sound ends, so the threshold can be tuned from the daemon's log.
"""

import sys

from pymicro_wakeword import MicroWakeWord, MicroWakeWordFeatures, Model

# Ordinary speech stays below this score; anything above is worth reporting.
CANDIDATE = 0.3
CHUNK_BYTES = 2048


def main() -> None:
    model = MicroWakeWord.from_builtin(Model(sys.argv[1]))
    threshold = float(sys.argv[2])
    features = MicroWakeWordFeatures()
    peak, hit, steps = 0.0, False, 0
    while chunk := sys.stdin.buffer.read(CHUNK_BYTES):
        for feature in features.process_streaming(chunk):
            score = model.process_streaming_prob(feature)
            steps += 1
            # The model scores every `stride` features and returns 0.0 in between.
            if score is None or steps % model.stride:
                continue
            if score >= CANDIDATE:
                peak = max(peak, score)
                if not hit and score > threshold:
                    hit = True
                    print(f"hit {score:.3f}", flush=True)
            elif peak:
                print(f"score {peak:.3f}", flush=True)
                peak, hit = 0.0, False


if __name__ == "__main__":
    main()
