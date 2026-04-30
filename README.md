# microagent2

A multi-service agent runtime built around a local LLM, with first-class support for Anthropic-compatible skills, persistent memory via [Hindsight](https://github.com/vectorize-io/hindsight), and a sandboxed code-execution service the agent can author new skills inside of.

**Status:** active development. Most core services are implemented; see `docs/` for design direction and `openspec/specs/` for the current contract.

## What's in the box

```
┌──────────────────────────────────────────────────────────────┐
│                   microagent2 architecture                   │
│                                                              │
│   Client (open-webui, curl, any OpenAI-compat SDK)           │
│       │                                                      │
│       ▼                                                      │
│   gateway ──── context-manager ──── memory-service ─▶ Hindsight
│       │              │                                       │
│       ▼              │                                       │
│   llm-broker ── main-agent ──── exec ── skills/              │
│       │                           ▲                          │
│       ▼                           │                          │
│   llm-proxy ─▶ local LLM        /sandbox (per-session,       │
│                (llama.cpp, ollama,         agent-authoring)  │
│                 etc.)                                        │
│                                                              │
│   retro-agent (background retrospection loop)                │
└──────────────────────────────────────────────────────────────┘
```

Eight Go services speak to each other over Valkey streams + HTTP. The user talks to the gateway; the gateway assembles context, routes to main-agent, which runs a tool loop against the LLM through the broker. Tools include skill discovery, memory reads, script execution, and arbitrary shell inside a per-session sandbox.

## Quick start

```bash
# 1. Clone and configure
git clone https://github.com/jawillis/microagent2.git
cd microagent2
cp .env.example .env
# Edit .env: set LLAMA_SERVER_ADDR to your inference endpoint,
# LLAMA_API_KEY if required, and any other values you want to override.

# 2. Bring up the stack
docker compose up -d

# 3. Chat via open-webui
open http://localhost:3000
```

The default compose stack starts: `valkey`, `gateway`, `context-manager`, `hindsight`, `memory-service`, `llm-broker`, `llm-proxy`, `main-agent`, `retro-agent`, `exec`, and `open-webui`. The `mitmweb` service is opt-in under the `debug` profile for traffic inspection.

### Running pre-built images (no local builds)

`docker-compose.ghcr.yml` runs the same stack against the multi-arch images published to GitHub Container Registry on every push to `main`:

```bash
docker compose -f docker-compose.ghcr.yml up -d
```

Override the registry namespace or pin a specific tag via env:

```bash
MICROAGENT2_REGISTRY_OWNER=jawillis    # default — change for forks
MICROAGENT2_TAG=latest                 # override with e.g. sha-abc1234 for reproducible deploys
```

## Workflow: OpenSpec-driven changes

This project uses [OpenSpec](https://openspec.dev) for structured change management. Every non-trivial change gets a proposal, a design, a spec delta, and a task list before implementation. Archived changes live under `openspec/changes/archive/` and the canonical contract lives in `openspec/specs/`.

```bash
/opsx:explore <idea>      # Think it through before committing
/opsx:propose <name>      # Create proposal + design + spec delta + tasks
/opsx:apply <name>        # Implement the task list
/opsx:archive <name>      # Sync deltas into main specs, archive the change
```

## Design documents

Living docs for the major subsystems — read these for the "why", `openspec/specs/` for the "what":

| Doc | Topic |
|-----|-------|
| [`docs/memory-system-design.md`](docs/memory-system-design.md) | Hindsight as memory substrate, multi-speaker identity, three-axis fact attribution |
| [`docs/curiosity-and-initiative.md`](docs/curiosity-and-initiative.md) | Agent idle-time research, proactive conversation |
| [`docs/self-improvement-framework.md`](docs/self-improvement-framework.md) | Policy registry, reward modeling, LoRA pipeline |
| [`docs/skills-runtime-design.md`](docs/skills-runtime-design.md) | Anthropic agent-skills parity + the `exec` code-execution service |

## Repository layout

```
cmd/                   # Go service entrypoints (one per service)
  context-manager/
  exec/
  gateway/
  llm-broker/
  llm-proxy/
  main-agent/
  memory-service/
  retro-agent/
internal/              # Shared Go packages; keyed by capability
  agent/ broker/ dashboard/ exec/ execclient/ gateway/
  hindsight/ llmproxy/ logstream/ mcp/ memoryclient/
  memoryservice/ messaging/ registry/ response/ retro/
  sessionskill/ skills/ tools/
deploy/                # Per-service Dockerfiles
docs/                  # Design documents
openspec/              # Spec + change artifacts
  changes/             # Active changes
    archive/           # Completed, synced changes (YYYY-MM-DD-<name>/)
  specs/               # Current contract
skills/                # Checked-in agent skills (Anthropic-compatible)
.github/workflows/     # CI: builds and publishes multi-arch images to GHCR
```

## Building

```bash
go build ./...
go test ./...
go test -race ./...             # mandatory for concurrent packages
docker compose build            # build all Docker images locally
```

Pre-built images are published to `ghcr.io/<owner>/microagent2-<service>` on every push to `main`. See `.github/workflows/build-images.yml`.

## License

MIT — see [LICENSE](LICENSE).
