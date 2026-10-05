<script lang="ts">
  const PRICE_PER_MINUTE = 0.05;
  const DSP = [
    ["echoCancellation", "AEC"],
    ["noiseSuppression", "Шумодав"],
    ["autoGainControl", "Автоусиление"],
  ] as const;
  const STATUS = { closing: "Завершаю…", connecting: "Подключение…", idle: "", live: "Можно говорить" };

  interface LiveEvent {
    type: string;
    delta?: string;
    reason?: string;
    usage?: { seconds: number };
    error?: { message: string };
  }

  let inputs = $state<MediaDeviceInfo[]>([]);
  let outputs = $state<MediaDeviceInfo[]>([]);
  let inputId = $state("default");
  let outputId = $state("default");
  let dsp = $state({ autoGainControl: true, echoCancellation: true, noiseSuppression: true });

  let phase = $state<keyof typeof STATUS>("idle");
  let error = $state("");
  let you = $state("");
  let gpt = $state("");
  let seconds = $state(0);
  let recording = $state("");

  let audio: HTMLAudioElement;
  let peer: RTCPeerConnection | undefined;
  let events: RTCDataChannel | undefined;
  let mic: MediaStream | undefined;
  let autoStop: ReturnType<typeof setTimeout>;

  async function refreshDevices() {
    const all = await navigator.mediaDevices.enumerateDevices();
    inputs = all.filter((d) => d.kind === "audioinput");
    outputs = all.filter((d) => d.kind === "audiooutput");
  }

  // Chrome hides device names and lists only placeholders until the page gets microphone access.
  async function loadDevices() {
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      for (const track of stream.getTracks()) {
        track.stop();
      }
    } catch {
      // Without access the lists keep unnamed placeholders; Start reports the actual error.
    }
    await refreshDevices();
  }

  loadDevices();
  navigator.mediaDevices.addEventListener("devicechange", refreshDevices);

  async function setSink(id: string) {
    try {
      await audio.setSinkId(id);
    } catch (sinkError) {
      error = `Динамик: ${(sinkError as Error).message}`;
    }
  }

  $effect(() => {
    setSink(outputId);
  });

  function finish(reason = "") {
    if (phase === "idle") {
      return;
    }
    phase = "idle";
    if (reason) {
      error = reason;
    }
    clearTimeout(autoStop);
    // Ending the tracks also stops the recorder.
    for (const track of mic?.getTracks() ?? []) {
      track.stop();
    }
    // Closing the peer closes the data channel too.
    peer?.close();
  }

  // Keeps the connection open until session.closed reports the final usage.
  function stop() {
    phase = "closing";
    clearTimeout(autoStop);
    autoStop = setTimeout(() => finish("OpenAI не подтвердил закрытие, итоговое время неизвестно"), 15_000);
    events?.send(JSON.stringify({ type: "session.close" }));
  }

  // Records what the browser sends to WebRTC: device DSP plus the browser's AEC/NS/AGC.
  function record(stream: MediaStream) {
    const chunks: Blob[] = [];
    const recorder = new MediaRecorder(stream);
    recorder.addEventListener("dataavailable", (e) => chunks.push(e.data));
    recorder.addEventListener("stop", () => {
      recording = URL.createObjectURL(new Blob(chunks, { type: recorder.mimeType }));
    });
    recorder.start();
  }

  function onEvent(e: LiveEvent) {
    if (e.type === "session.started") {
      phase = "live";
      // The session bills even in silence.
      autoStop = setTimeout(stop, 10 * 60_000);
      if (mic) {
        record(mic);
      }
      // The microphone keeps streaming meanwhile, so the user can cut the greeting short.
      events?.send(
        JSON.stringify({
          content: "Поздоровайся сейчас по-русски одной короткой фразой и спроси, чем помочь. Потом замолчи и слушай.",
          delegation_id: null,
          event_id: "greeting",
          type: "session.instructions.append",
        }),
      );
    }
    if (e.type === "session.input_transcript.delta") {
      you += e.delta ?? "";
    }
    if (e.type === "session.output_transcript.delta") {
      gpt += e.delta ?? "";
    }
    if (e.type === "session.usage.updated" || e.type === "session.closed") {
      seconds = e.usage?.seconds ?? seconds;
    }
    // expired, content (safety filter), connection_lost: say why the session ended on its own.
    if (e.type === "session.closed") {
      finish(e.reason === "close_requested" ? "" : `Сессия закрыта: ${e.reason}`);
    }
    if (e.type === "error") {
      error = e.error?.message ?? "Неизвестная ошибка";
    }
  }

  async function start() {
    phase = "connecting";
    error = "";
    you = "";
    gpt = "";
    URL.revokeObjectURL(recording);
    recording = "";
    seconds = 0;
    const stream = await navigator.mediaDevices.getUserMedia({ audio: { deviceId: { exact: inputId }, ...dsp } });
    mic = stream;

    const pc = new RTCPeerConnection();
    peer = pc;
    pc.addEventListener("track", (e) => {
      audio.srcObject = new MediaStream([e.track]);
    });
    pc.addEventListener("connectionstatechange", () => {
      if (pc.connectionState === "failed") {
        finish("WebRTC-соединение оборвалось");
      }
    });
    for (const track of stream.getTracks()) {
      pc.addTrack(track, stream);
    }
    events = pc.createDataChannel("oai-events");
    events.addEventListener("message", (e) => onEvent(JSON.parse(e.data)));
    events.addEventListener("close", () => finish("Соединение оборвалось"));

    await pc.setLocalDescription(await pc.createOffer());
    if (pc.iceGatheringState !== "complete") {
      await new Promise<void>((resolve) => {
        pc.addEventListener("icegatheringstatechange", () => {
          if (pc.iceGatheringState === "complete") {
            resolve();
          }
        });
      });
    }
    const offer = pc.localDescription;
    if (!offer) {
      throw new Error("WebRTC не подготовил SDP-предложение");
    }
    const res = await fetch("/api/session", { body: offer.sdp, method: "POST" });
    if (!res.ok) {
      throw new Error(await res.text());
    }
    const answer: { transport: { sdp: string } } = await res.json();
    // The HTTP request already started the session: never send session.start.
    await pc.setRemoteDescription({ sdp: answer.transport.sdp, type: "answer" });
  }
</script>

<main>
  <header>
    <h1>GPT-Live Speaker</h1>
    <p role="status" class:error data-phase={phase}>{error || STATUS[phase]}</p>
  </header>

  <section class="panel">
    <label>
      Микрофон
      <select bind:value={inputId} disabled={phase !== "idle"}>
        {#each inputs as d (d.deviceId)}<option value={d.deviceId}>{d.label}</option>{/each}
      </select>
    </label>
    <label>
      Динамик
      <select bind:value={outputId} disabled={phase !== "idle"}>
        {#each outputs as d (d.deviceId)}<option value={d.deviceId}>{d.label}</option>{/each}
      </select>
    </label>
    <div class="row">
      {#each DSP as [key, name] (key)}
        <label class="check">
          <input type="checkbox" bind:checked={dsp[key]} disabled={phase !== "idle"} />{name}
        </label>
      {/each}
    </div>
  </section>

  <section class="row">
    {#if phase === "idle"}
      <button onclick={() => start().catch((e) => finish(e.message || e.name))}>Старт</button>
    {:else}
      <button onclick={stop} disabled={phase !== "live"}>Стоп</button>
    {/if}
    <span class="meter">{seconds} с · ${((seconds / 60) * PRICE_PER_MINUTE).toFixed(3)}</span>
    {#if recording}<a href={recording} download="mic.webm">Запись микрофона</a>{/if}
  </section>

  <section class="transcripts">
    <article><h2>Ты</h2><div class="scroll"><p>{you.trimStart()}</p></div></article>
    <article><h2>GPT</h2><div class="scroll"><p>{gpt.trimStart()}</p></div></article>
  </section>

  <audio bind:this={audio} autoplay></audio>
</main>

<style>
  :global(:root) {
    color-scheme: light dark;
    font: 15px/1.5 system-ui, sans-serif;
    --line: color-mix(in oklab, currentColor 15%, transparent);
    /* L 0.5 keeps white button text above 4.5:1 contrast. */
    --accent: oklch(0.5 0.17 255);
  }
  main {
    max-width: 760px;
    margin: 0 auto;
    padding: 24px 20px;
    display: grid;
    gap: 20px;
  }
  header {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
  }
  h1,
  p {
    margin: 0;
  }
  h1 {
    font-size: 20px;
  }
  h2 {
    margin: 0 0 6px;
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    opacity: 0.6;
  }
  /* Light variants use L 0.5 to stay above 4.5:1 contrast on white. */
  [data-phase="live"] {
    color: light-dark(oklch(0.5 0.17 150), oklch(0.65 0.17 150));
  }
  [data-phase="connecting"],
  [data-phase="closing"] {
    color: light-dark(oklch(0.5 0.15 75), oklch(0.72 0.15 75));
  }
  .error {
    color: light-dark(oklch(0.5 0.2 25), oklch(0.62 0.2 25));
  }
  .panel {
    display: grid;
    gap: 12px;
    padding: 16px;
    border: 1px solid var(--line);
    border-radius: 12px;
  }
  label {
    display: grid;
    gap: 4px;
    font-size: 13px;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 16px;
  }
  .check {
    display: flex;
    gap: 6px;
  }
  select {
    font: inherit;
    padding: 6px 10px;
    border: 1px solid var(--line);
    border-radius: 8px;
    background: transparent;
    color: inherit;
  }
  button {
    font: inherit;
    font-weight: 600;
    padding: 8px 20px;
    border: 0;
    border-radius: 8px;
    background: var(--accent);
    color: white;
    cursor: pointer;
  }
  button:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .meter {
    font-variant-numeric: tabular-nums;
    opacity: 0.75;
  }
  .transcripts {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 16px;
  }
  .scroll {
    height: 320px;
    overflow-y: auto;
    display: flex;
    flex-direction: column-reverse; /* keeps the newest text in view */
    padding: 12px;
    border: 1px solid var(--line);
    border-radius: 12px;
  }
  .scroll p {
    white-space: pre-wrap;
  }
</style>
