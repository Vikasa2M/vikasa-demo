# Getting Started

This is the end-to-end path for someone who has never run the demo before:
install the two prerequisites, bring the stack up, look around, and (optionally)
run the "the model is all you need" AI segment. Every command below says what it
actually does, so you're not pasting things blind.

For the deeper operational reference — deployment sizes, the recording tour,
full troubleshooting — see [`RUNBOOK.md`](RUNBOOK.md). This page is the on-ramp.

## What you end up with

A faithful, laptop-sized copy of the Vikasa reference architecture running
entirely on your machine: three state DOTs (Marren/Velia/Sabine), each with a full
cabinet → regional → central → DMZ NATS JetStream chain, federated into one
shared ClickHouse + Grafana view. Then, if you want it, a model that builds its
own Grafana dashboard from the data model alone.

Nothing leaves your laptop. There is no cloud account to create for the base
demo.

---

## 1. Install the prerequisites

The base demo needs exactly two things: **Docker** and **Go**. It is
self-contained otherwise — no sibling repositories, no manual dependency
fetching (see [`MODELS-PIN.md`](MODELS-PIN.md) for why).

> **Platform note.** This demo is **validated on macOS** (Apple Silicon and
> Intel). The toolchain is just Docker, `make`, `bash`, and Go, so **Linux** and
> **Windows (WSL2)** are expected to work the same way — but **neither has been
> validated yet**. The steps below are written for macOS/Linux; the Windows path
> is best-effort. Reports from either are welcome.

> **On Windows?** Read [Running on Windows (WSL2)](#running-on-windows-wsl2)
> first — you'll install and run everything *inside* WSL2, and the steps below
> are then exactly the Linux steps.

### Docker Desktop

The whole stack runs as Docker containers. Install Docker Desktop (or any
Docker Engine ≥ 24) from <https://www.docker.com/products/docker-desktop/>.

Two resource settings matter, both under Docker Desktop → Settings → Resources:

- **Memory:** allow **~12 GB** for the full fleet (steady state is ~6 GB, but
  leave headroom). A smaller deployment (below) needs far less — ~4 GB is fine.
- **Disk:** the Docker VM has its own virtual disk, and it must have **free
  space**. If that disk fills up, ClickHouse won't start and Prometheus crashes
  on boot — a confusing failure that looks like a bug but is just a full disk.
  Check with `docker system df`; reclaim space with `docker builder prune -af`
  and `docker image prune -f` if it's tight.

Verify Docker is running:

```sh
docker info        # should print server details, not a connection error
```

### Go

The demo's control CLI (`democtl`), the cabinet simulators, and the sinks are
Go programs that `make demo` builds and runs. You need **Go 1.26** (the version
pinned in `go.mod`).

- **macOS:** `brew install go` (Homebrew), or download from <https://go.dev/dl/>.
- **Linux:** your distro's package may lag; the tarball from <https://go.dev/dl/>
  is the reliable route.

Verify:

```sh
go version         # should report go1.26 or newer
```

That's the full prerequisite list for the base demo. The AI segment needs a
couple more things — covered in step 5.

---

## Running on Windows (WSL2)

> **Not yet validated.** The demo is validated on macOS; this WSL2 path is
> expected to work (same containers, same toolchain) but hasn't been run
> end-to-end on Windows yet. Treat it as best-effort and please file anything
> that trips you up.

The stack runs in Docker on Windows via WSL2 much as it does on macOS/Linux. The
only thing that isn't Windows-native is the **control tooling** — `make` and two
POSIX shell scripts (`scripts/demo-up.sh`, `tools/ai/gen-models-pack.sh`). The
clean fix is **WSL2** (Windows Subsystem for Linux): it gives you `make`,
`bash`, and `go` as native Linux, and Docker Desktop already uses WSL2 as its
backend, so they integrate out of the box. Run *everything* inside WSL2 — don't
try to drive the Makefile from PowerShell or cmd, and you don't need a separate
"tooling container," WSL2 is that environment.

1. **Install WSL2 + Ubuntu.** In an admin PowerShell:
   ```powershell
   wsl --install
   ```
   This installs WSL2 and Ubuntu; reboot when it asks.
2. **Install Docker Desktop for Windows**, then in Docker Desktop →
   Settings → Resources → **WSL Integration**, enable integration for your
   Ubuntu distro. That puts the `docker` CLI on your PATH inside WSL.
3. **Size the VM.** Docker Desktop on Windows draws its memory from WSL2. Create
   `C:\Users\<you>\.wslconfig` with, e.g.:
   ```ini
   [wsl2]
   memory=12GB
   ```
   then run `wsl --shutdown` (PowerShell) to apply. Same RAM/disk guidance as
   above.
4. **Open the Ubuntu shell** (from the Start menu) and install Go + make/git:
   ```sh
   sudo apt update && sudo apt install -y make git
   # Go 1.26: apt's version may lag — the tarball from https://go.dev/dl/ (linux-amd64) is reliable
   go version
   ```
5. **Clone into the WSL filesystem.** A public clone needs no auth — just
   `git clone <repo-url> ~/vikasa-demo` from inside Ubuntu. (If you later work
   against a private fork, set up WSL's own GitHub credentials once — the
   least-fuss way is the GitHub CLI, which also wires up git's credential
   helper: `sudo apt install -y gh && gh auth login`, then
   `gh repo clone <org>/vikasa-demo ~/vikasa-demo`.)
   Clone into the WSL filesystem (`~/...`), **not** under `/mnt/c/...` — the
   Windows drive makes file I/O and Docker bind-mounts dramatically slower.

   Two alternatives if you'd rather not run `gh auth login`:
   - **Reuse your Windows GitHub login** by pointing WSL's git at the Windows
     Git Credential Manager (ships with Git for Windows):
     `git config --global credential.helper "$(which git-credential-manager || echo /mnt/c/'Program Files'/Git/mingw64/bin/git-credential-manager.exe)"`,
     then clone over HTTPS — it reuses your existing credentials, no new login.
   - **Clone on Windows, copy into WSL.** If auth is already set up on the
     Windows side, clone there and then
     `cp -r /mnt/c/Users/<you>/vikasa-demo ~/vikasa-demo` — you get the fast WSL
     filesystem with no second auth setup. Trade-off: the WSL copy can't
     `git pull` (re-copy to update).
6. **Run everything as on Linux** — `make demo`, `go run ./cmd/democtl ...`, all
   the commands below, from the Ubuntu shell.
7. **Open Grafana from your Windows browser** at <http://localhost:3000> — WSL2
   forwards `localhost` to Windows automatically.

For the **AI segment** on Windows, the least-friction path is to run a CLI
client **inside WSL too** (Claude Code or Codex CLI), so the client, the two MCP
servers, and the stack all live in the same WSL environment. Claude Desktop is a
Windows GUI app and can't run in WSL — it works, but then the client (Windows)
and servers span the Windows/WSL boundary, which is more to reason about.
`host.docker.internal:3000` (used by the Grafana MCP server) resolves on Windows
Docker Desktop the same as on macOS.

---

## 2. Get the code

```sh
git clone <this-repo-url> vikasa-demo
cd vikasa-demo
```

---

## 3. Bring the stack up

```sh
make demo          # the full ~99-cabinet fleet
```

`make demo` runs the whole bring-up in order. Concretely, it:

1. **Builds the demo image** — one image holds all the Go binaries
   (`cabinet-sim`, the sinks, `democtl`).
2. **Generates the topology** from `deploy/topology/fleet.yaml` (the single
   source of truth for the fleet). This renders the docker-compose overlay, the
   per-server NATS configs, the per-cabinet simulator configs, and the
   ClickHouse dimension seed. All generated files are gitignored — they're
   rebuilt from `fleet.yaml` every time.
3. **Brings up shared infra** — ClickHouse, Grafana, Prometheus.
4. **Applies the ClickHouse schema** and dimension seed.
5. **Starts the NATS tiers, cabinet leaves, and simulators**, in paced batches
   (starting ~200 containers at once overwhelms the Docker daemon, so the
   bring-up is deliberately batched — see `scripts/demo-up.sh`).
6. **Applies each DOT's stream configs**, then starts the per-DOT sinks and the
   federation sink once their streams exist.

Expect **~6–8 minutes** the first time (the image build dominates). When it
finishes it prints how many containers came up.

### Pick a smaller size if your machine is modest

The full fleet is ~226 containers. If that's too heavy, bring up a smaller
footprint instead:

```sh
make demo-small    # 3 cabinets (~34 containers), ~1–2 min
make demo-medium   # 18 cabinets (~64 containers), ~2–3 min — runs the whole tour
```

`make demo` (large) is the default. See the RUNBOOK's
[Deployment sizes](RUNBOOK.md#deployment-sizes-small--medium--large) section for
what each size includes and which demo phases it supports.

---

## 4. Confirm it came up clean

```sh
go run ./cmd/democtl verify baseline --dot mardot
go run ./cmd/democtl verify baseline --dot veldot
go run ./cmd/democtl verify baseline --dot sabdot
```

`democtl verify baseline` checks the pipeline is actually moving data: recent
events are landing, heartbeats are fresh, and there are no dead letters. Each
DOT should print `[PASS]` for all three checks.

Then open **Grafana at <http://localhost:3000>** — anonymous access is enabled
with an Admin role, so there's no login. The dashboards live in the "Vikasa"
folder (full list and URLs in the [RUNBOOK](RUNBOOK.md#dashboards-grafana-vikasa-folder)).

If a `verify baseline` fails or the stack looks stale, the most common causes
(full Docker disk; a stack left running across a laptop sleep) are in the
RUNBOOK's [Troubleshooting](RUNBOOK.md#troubleshooting) section.

---

## 5. The AI segment ("the model is all you need")

Optional, and the most fun part. The idea: hand a model **only the data model**
(the YANG modules) plus two tools over MCP — a read-only ClickHouse server and a
Grafana server scoped to one folder — and it discovers the schema itself and
builds a working operations dashboard from first principles. No schema docs, no
example dashboard to copy.

This part needs a few things beyond Docker + Go:

- **An MCP client** to drive the model (see the client options in step 5c).
- **[`uv`](https://docs.astral.sh/uv/)** — runs the ClickHouse MCP server with
  no install (`brew install uv` or the installer on that page).
- Docker (already installed) — runs the Grafana MCP server.

### 5a. `democtl ai-setup` — issue scoped credentials

```sh
go run ./cmd/democtl ai-setup
```

This prepares the Grafana side and prints a ready-to-paste block of environment
variables. Specifically it:

- Creates the **"AI Built"** Grafana folder (if absent).
- Creates a **service account scoped to only that folder** and issues an API
  token for it. The token can create and edit dashboards **in "AI Built" and
  nowhere else** — if the model (or a typo) tries to touch anything else in
  Grafana, it gets a 403.
- Prints that token alongside the **read-only ClickHouse** credentials
  (`ai_readonly`, provisioned server-side with `readonly=1` in the ClickHouse
  config — it can query but never mutate).

It's idempotent — re-run it any time; it rotates the token. Paste the printed
block into your MCP client's config (step 5c).

### 5b. `make ai-models-pack` — build the data-model file

```sh
make ai-models-pack
```

This concatenates the OpenITS YANG modules (signal-control, traffic-sensor,
perception, dms, and the common/types base modules) into a single file,
`demo/ai/models-pack.generated.yang` (~300 KB, gitignored). That one file is
**everything the model is given** about the domain — the same modules the demo's
event schema and ClickHouse tables were generated from, so the model can
correlate YANG leaves back to columns.

Note: this is the one AI-segment step that isn't fully self-contained — it reads
the pinned models checkout (`../../openits-models-pinned` via `MODELS_DIR`). See
[`MODELS-PIN.md`](MODELS-PIN.md). The base demo does not need it.

### 5c. Wire the two MCP servers into your client

If you're using **Claude Code or Codex**, there's a one-step wiring script —
it runs `ai-setup` and registers both servers for you:

```sh
make ai-wire-claude          # or: make ai-wire-codex
# preview without changing anything: ./tools/ai/wire-mcp.sh claude --dry-run
```

Otherwise, the full per-client wiring lives in
[`demo/ai/mcp/README.md`](../demo/ai/mcp/README.md). Which client you use decides
how much setup this is:

| Client | MCP model | Setup |
|---|---|---|
| **Claude Code / Claude Desktop** | Local (stdio) | Easiest — the app runs the two MCP servers on your machine directly. No tunnels. |
| **Codex CLI** (OpenAI) | Local (stdio) | Also easy — `codex mcp add …` runs the servers locally, same as Claude. Reads the YANG pack as a local file (no upload). |
| **ChatGPT app** (Developer Mode) | Remote only | More setup — ChatGPT's MCP client runs in OpenAI's cloud and can't reach your `localhost`, so you must run the servers in HTTP mode and expose them over an HTTPS tunnel (ngrok / Cloudflare / Secure MCP Tunnel). |
| **Local model via Ollama** | Local (custom agent) | Possible but unreliable — see the note below. |

The credentials are the same across all of them — the `ai-setup` token and the
read-only ClickHouse creds. Only the transport differs.

**On running it fully local with Ollama:** you *can* drive the segment with a
local model (e.g. `qwen3.6:35b`) by pairing Ollama with a small agent that
speaks MCP — [Pydantic AI](https://ai.pydantic.dev/) is a clean fit, since it
bundles the agent loop, an MCP client, and an Ollama provider. In testing this
genuinely worked in part — the model discovered the schema, wrote correct
ClickHouse SQL, and built a dashboard — but it was **not reliable**: it fell into
query-repetition loops, sometimes narrated its plan instead of calling the tool,
and got the ClickHouse panel field wrong (`expr` instead of `rawSql`). It needs
a raised tool-retry limit, anti-repetition sampling, and realistically some
output-repair scaffolding, and even then it didn't cleanly pass
`verify ai-dashboard`. For a dependable take, use a frontier model (Claude via
Claude Code/Desktop, or an OpenAI model via Codex); treat Ollama as an
"it even runs offline" bonus, not the primary path.

### 5d. Give the model the task

Once the tools are wired, attach `demo/ai/models-pack.generated.yang`, send
[`demo/ai/prompts/system-models-only.md`](../demo/ai/prompts/system-models-only.md)
as the system prompt, and give it
[`demo/ai/prompts/task-corridor.md`](../demo/ai/prompts/task-corridor.md) as the
task. It will explore the schema over the ClickHouse tool, verify its queries,
and build the dashboard in the "AI Built" folder via the Grafana tool.

### 5e. `democtl verify ai-dashboard` — the take-QA gate

```sh
go run ./cmd/democtl verify ai-dashboard
```

This is the honest check on the model's work. It finds the newest dashboard in
the "AI Built" folder, pulls each panel's ClickHouse query (`rawSql`), expands
Grafana's `$__timeFilter` macro into a real time bound, and runs every query
against ClickHouse. It passes only if **every panel query returns data** — i.e.
the dashboard isn't just a shell, its panels actually resolve against the live
data.

Reset the folder between attempts with `go run ./cmd/democtl ai-reset`.

---

## 6. Tear it all down

```sh
make demo-down
```

Removes every container, network, and volume for the project. The next
`make demo` starts completely fresh, including an empty ClickHouse.

Tearing down and bringing back up is also the fix if the stack goes stale (for
example after the laptop sleeps — the simulators' event clocks stop advancing,
and `verify baseline` starts failing on `recent-events`).

---

## Where to go next

- [`RUNBOOK.md`](RUNBOOK.md) — deployment sizes, the recorded tour, ports, full
  troubleshooting.
- [`demo/ai/mcp/README.md`](../demo/ai/mcp/README.md) — per-client MCP wiring for
  the AI segment.
