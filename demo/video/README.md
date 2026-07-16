# Automated walkthrough screencast

Renders a silent ~4.5-min screencast of the demo by driving Grafana with
Playwright, firing the live perception inject at the right beat, and burning a
lower-third caption onto each scene. Output: `demo/vikasa-walkthrough.mp4`
(1920×1080, 30 fps, H.264). The captions carry the story — the video is silent
so a presenter can narrate over it, or it stands alone as reference b-roll.

## What it shows (run-of-show)

Intro card → **Act 1** exec Federation + Multi-Vendor → **Act 2** Fleet Health +
Demo Tour → **Act 3** Resilience Lab → **Act 4** Perception (with a *live*
`corridor-incident` inject — cab-i85-001 turns red on camera), Reversible Lanes,
Signal Performance → **Act 5** a terminal types the real AI prompt and the model
"builds" the dashboard, then cuts to the actual AI-built dashboard → outro card.

See `docs/WALKTHROUGH.md` for the full presenter run-of-show this mirrors.

## Honesty note

Every dashboard shown is live Grafana against real pipeline data, and the Act 4
perception incident is a genuine inject propagating through NATS → ClickHouse →
Grafana during the recording. The Act 5 **terminal scene is a faithful
reenactment**: the typed prompt is verbatim from `demo/ai/prompts/`, and the
status lines mirror the model's real method (read YANG → discover schema →
verify queries → publish), but they are a stylized depiction, not a live capture
of the model working. The dashboard it cuts to (`ai-corridor-agent`) is the real
one the model built earlier. To show the model building live, screen-capture an
actual MCP agent session instead (see `docs/RUNBOOK.md` AI section).

## Regenerate

Requires the stack up (`make demo`) and ~40 min of accumulated data so the
time-series look full. Node 22+ and ffmpeg 8+ on PATH.

```sh
cd demo/video
npm install playwright@1.61.1                 # reuses cached Chromium
PLAYWRIGHT_BROWSERS_PATH="$HOME/Library/Caches/ms-playwright" node record.js
# then transcode the webm it prints to mp4:
ffmpeg -y -i page@*.webm -vf "fps=30,format=yuv420p" -c:v libx264 -crf 20 \
  -preset medium -movflags +faststart ../vikasa-walkthrough.mp4
```

Tune scene order, captions, and dwell times in the `scenes` array in
`record.js`. The intro/outro/terminal `.html` files are self-contained cards.
