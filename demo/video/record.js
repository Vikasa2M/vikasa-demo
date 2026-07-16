// Automated silent screencast of the Vikasa demo, driven through Grafana with
// Playwright. Walks the run-of-show acts, performs a LIVE WAN cut + restore in
// Act 3, fires the live perception inject in Act 4, and marks each scene with a
// small top-left act chip (the video is silent — a presenter narrates over it).
// Output: one continuous webm; convert to mp4 with ffmpeg afterwards.
//
// Env overrides:
//   OUT_DIR       where to write the .webm (default: this dir)
//   VIKASA_REPO  repo root for `go run ./cmd/democtl` (default: ../.. from here)
const { chromium } = require('playwright');
const { execFile } = require('child_process');
const path = require('path');

const G = 'http://localhost:3000';
const ASSET_DIR = __dirname;                                   // intro/outro/terminal cards live next to this file
const OUT_DIR = process.env.OUT_DIR || __dirname;              // webm output
const REPO = process.env.VIKASA_REPO || path.resolve(__dirname, '../..'); // has go.mod, for democtl
const W = 1920, H = 1080;
const T30 = 'from=now-30m&to=now';
const T15 = 'from=now-15m&to=now';
const SETTLE = 5500;      // panel render settle after navigation
const MAP_SETTLE = 8000;  // geomap dashboards need longer for OSM tiles

const scenes = [
  { card: 'intro.html', dwell: 4500 },
  { path: '/d/vikasa-exec-federation/x', q: T30, chip: 'Act 1 · Corridor Federation', dwell: 16000 },
  { path: '/d/vikasa-exec-multi-vendor/x', q: T30, chip: 'Act 1 · Open Standards', dwell: 14000 },
  { path: '/d/vikasa-fleet-health/x', q: `${T15}&var-dot=gdot`, map: true, chip: 'Act 2 · Fleet Health', dwell: 16000 },
  { path: '/d/vikasa-demo-tour/x', q: `${T15}&var-dot=gdot`, chip: 'Act 2 · Live Pipeline', dwell: 14000 },
  { path: '/d/vikasa-resilience-lab/x', q: T15, map: true, chip: 'Act 3 · Resilience', wancut: true },
  { path: '/d/vikasa-perception-fusion/x', q: T15, map: true, chip: 'Act 4 · Perception · I-85', dwell: 8000,
    inject: 'http://localhost:18081/inject/corridor-incident', postInjectDwell: 24000 },
  { path: '/d/vikasa-reversible-lanes/x', q: T15, map: true, chip: 'Act 4 · Reversible Lanes', dwell: 22000 },
  { path: '/d/vikasa-signal-performance/x', q: `${T15}&var-dot=gdot&var-cabinet=$__all`, chip: 'Act 4 · Signal Performance', dwell: 16000 },
  { card: 'terminal.html', dwell: 20000 },
  { path: '/d/ai-corridor-agent/x', q: T15, map: true, chip: 'Act 5 · AI-Built Dashboard', dwell: 18000 },
  { card: 'outro.html', dwell: 6500 },
];

// chipFn runs IN the page: renders/replaces a small top-left act chip.
function chipFn(text) {
  const old = document.getElementById('__chip');
  if (old) old.remove();
  const d = document.createElement('div');
  d.id = '__chip';
  d.textContent = text.toUpperCase();
  Object.assign(d.style, {
    position: 'fixed', top: '11px', left: '14px', zIndex: '2147483647',
    padding: '8px 16px', background: 'rgba(13,17,23,.94)', border: '1px solid #2a3546',
    borderLeft: '3px solid #6ea8fe', borderRadius: '8px', color: '#e8eef7',
    fontFamily: '-apple-system,Helvetica,Arial,sans-serif', fontSize: '15px', fontWeight: '700',
    letterSpacing: '.14em', pointerEvents: 'none', whiteSpace: 'nowrap',
    boxShadow: '0 6px 18px rgba(0,0,0,.45)',
  });
  document.body.appendChild(d);
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// runDemoctl execs the real democtl (cut/restore) from the repo root so the WAN
// cut in Act 3 is a genuine network partition, not a fake.
function runDemoctl(args) {
  return new Promise((res) => {
    console.log('  democtl', args.join(' '));
    execFile('go', ['run', './cmd/democtl', ...args], { cwd: REPO, timeout: 180000 }, (e, so, se) => {
      if (e) console.log('  democtl ERROR:', e.message.split('\n')[0], (se || '').slice(0, 300));
      else console.log('  democtl ok:', ((so || '') + (se || '')).trim().split('\n').slice(-1)[0]);
      res();
    });
  });
}

(async () => {
  const browser = await chromium.launch({ headless: true });
  const ctx = await browser.newContext({
    viewport: { width: W, height: H }, deviceScaleFactor: 1,
    recordVideo: { dir: OUT_DIR, size: { width: W, height: H } },
  });
  const page = await ctx.newPage();
  const video = page.video();

  for (const s of scenes) {
    if (s.card) {
      console.log(`[scene] card ${s.card}`);
      await page.goto('file://' + path.join(ASSET_DIR, s.card), { waitUntil: 'load' });
      await sleep(s.dwell);
      continue;
    }
    const url = `${G}${s.path}?kiosk&${s.q}&refresh=5s`;
    console.log(`[scene] ${s.chip}  ->  ${s.path}`);
    try {
      await page.goto(url, { waitUntil: 'load', timeout: 30000 });
    } catch (e) { console.log(`  goto slow (${e.message.split('\n')[0]}), continuing`); }
    await sleep(s.map ? MAP_SETTLE : SETTLE);
    await page.evaluate(chipFn, s.chip);

    if (s.wancut) {
      await sleep(8000);                                   // baseline steady state
      await runDemoctl(['cut', '--cabinet', 'cab-i85-001']);
      await page.evaluate(chipFn, 'Act 3 · Fiber Cut — cab-i85-001 severed');
      await sleep(34000);                                  // ingest drops, edge buffer climbs
      await runDemoctl(['restore', '--cabinet', 'cab-i85-001']);
      await page.evaluate(chipFn, 'Act 3 · Restored — draining, zero loss');
      await sleep(32000);                                  // buffer drains, cumulative catches up
    } else if (s.inject) {
      await sleep(s.dwell);
      console.log(`  inject -> ${s.inject}`);
      try { const r = await fetch(s.inject, { method: 'POST' }); console.log(`  inject HTTP ${r.status}`); }
      catch (e) { console.log(`  inject failed: ${e.message}`); }
      await sleep(s.postInjectDwell || 15000);
    } else {
      await sleep(s.dwell);
    }
  }

  await ctx.close();
  await browser.close();
  const out = await video.path();
  console.log(`\nVIDEO_WEBM=${out}`);
})().catch((e) => { console.error('FATAL', e); process.exit(1); });
