# AgentOps — Technical Planning Document v1.0
**Production-Grade AI Agent Lifecycle Platform**
*March 2026*

> A developer platform for building, evaluating, and monitoring AI agents.
> Think: LangSmith + PromptLayer + LangGraph — but yours, from the ground up.

| Field | Value |
|---|---|
| Status | Planning ✔ |
| Owner | Vaibhav |
| Phase | v1 — Greenfield |
| Target Deploy | Docker + Cloud VM |

---

## Table of Contents

1. [Locked Architecture Decisions](#1-locked-architecture-decisions)
2. [System Architecture](#2-system-architecture)
3. [Core Module Specifications](#3-core-module-specifications)
   - 3.1 [Agent Runtime Engine](#31-agent-runtime-engine)
   - 3.2 [Prompt Versioning System](#32-prompt-versioning-system)
   - 3.3 [Evaluation Engine](#33-evaluation-engine)
   - 3.4 [Observability Layer](#34-observability-layer)
   - 3.5 [Memory System](#35-memory-system)
4. [Database Schema](#4-database-schema-postgresql)
5. [REST API Surface](#5-rest-api-surface-go--fiber)
6. [CI/CD Pipeline](#6-cicd-pipeline)
7. [Repository Structure](#7-repository-structure)
8. [Build Phases & Milestones](#8-build-phases--milestones)
9. [The Iteration Loop (SDLC Core)](#9-the-iteration-loop-sdlc-core)
10. [V2 Backlog](#10-v2-backlog-deferred-not-forgotten)

---

## 1. Locked Architecture Decisions

> All decisions below are final and drive every design choice in this document. No revisiting without a documented reason.

| Decision Area | Choice | Rationale |
|---|---|---|
| Primary User | Solo developer (Vaibhav) | Portfolio + real use. No multi-tenancy in v1. |
| Deployment | Docker Compose local + single cloud VM | Ship fast, K8s is a v2 bonus signal. |
| Codebase | Greenfield — no legacy | Clean contracts from day 1. |
| Execution Model | Hybrid: LLM planner generates DAG, executor runs it | Most impressive model. Shows true agentic depth. |
| AI Provider | OpenRouter (primary) → Gemini (fallback) | Provider-agnostic by design. One API surface. |
| Tool Registry | JSON Schema in Postgres, loaded at runtime | Add tools without redeploy. Dynamic + safe. |
| Eval Metrics | Exact match + Cosine similarity + LLM-as-judge + Custom rules | Full spectrum. LLM-as-judge is industry standard. |
| Vector DB | Qdrant (self-hosted, Docker-native) | Lightweight, Go client, resume-worthy choice. |
| Observability | Custom trace table in Postgres + React UI | Own trace viewer is stronger signal than Jaeger plugin. |
| API Layer | REST primary; WebSocket (v2) for live trace streaming | Core right first. WS is a polish feature. |
| CI/CD | GitHub Actions + Eval regression gate + Docker push + Unit tests | Eval gate = automated quality enforcement. The differentiator. |
| Frontend | React + Vite + shadcn/ui + Recharts | Fast to build, accessible components, not generic-looking. |

---

## 2. System Architecture

### 2.1 High-Level Component Map

```
┌─────────────────────────────────────────────────────────┐
│               FRONTEND — React + Vite + shadcn/ui        │
│   Agent Builder | Prompt Studio | Eval Dashboard | Trace Viewer │
└─────────────────────────┬───────────────────────────────┘
                          │ REST API
┌─────────────────────────▼───────────────────────────────┐
│               API GATEWAY — Go + Fiber                   │
│    Auth middleware | Rate limiting | Request routing     │
│    Error handling                                        │
└───────────────────────────┬─────────────────────────────┘
                            │ Internal Go service calls
        ┌───────────────────┼───────────────────┐
        │                   │                   │
┌───────▼──────┐  ┌─────────▼──────┐  ┌────────▼────────┐  ┌────────────────┐
│ Agent Runtime │  │ Prompt Engine  │  │  Eval Engine    │  │ Observability  │
│               │  │                │  │                 │  │                │
│ Planner(DAG)  │  │ Version store  │  │ Test datasets   │  │ Trace store    │
│ Executor      │  │ Changelog      │  │ Scoring pipeline│  │ Step logging   │
│ Tool Router   │  │ Eval linking   │  │ LLM-as-judge    │  │ Token tracking │
│ Retry logic   │  │ Diff viewer    │  │ CI regression   │  │ Latency metrics│
└───────────────┘  └────────────────┘  └─────────────────┘  └────────────────┘
                            │
        ┌───────────────────┼───────────────────┐
        │                   │                   │
┌───────▼──────┐  ┌─────────▼──────┐  ┌────────▼────────┐
│  PostgreSQL  │  │    Qdrant       │  │     Redis       │
│              │  │                 │  │                 │
│ Agents       │  │ Long-term memory│  │ Session memory  │
│ Prompts      │  │ Episodic recall │  │ Execution cache │
│ Evals        │  │ Semantic search │  │ Rate limits     │
│ Traces       │  │                 │  │                 │
│ Tools | Runs │  │                 │  │                 │
└──────────────┘  └─────────────────┘  └─────────────────┘
```

### 2.2 Tech Stack Summary

| Layer | Technology | Notes |
|---|---|---|
| Backend | Go + Fiber | Fast, typed, production-grade HTTP framework |
| AI Layer | OpenRouter → Gemini fallback | Single API interface, multi-model support |
| Primary DB | PostgreSQL 16 | State, traces, prompts, evals, tools |
| Vector DB | Qdrant (Docker) | Long-term + episodic agent memory |
| Cache | Redis | Session state, execution cache, rate limits |
| Frontend | React + Vite + shadcn/ui | Recharts for eval dashboards |
| Infra | Docker Compose | All services containerized, single compose file |
| CI/CD | GitHub Actions | Eval gates, Docker push, unit tests on PR |
| Embeddings | OpenRouter embed or Gemini embed | For semantic eval scoring + memory retrieval |

---

## 3. Core Module Specifications

### 3.1 Agent Runtime Engine

The most technically complex module. Implements a **hybrid planning model**: an LLM generates a Directed Acyclic Graph (DAG) of steps, and a deterministic executor runs each node with full isolation, retry, and fallback logic.

#### Planner

- Receives: user goal + agent config + available tools
- Calls LLM (OpenRouter) with a structured system prompt instructing it to return a **JSON DAG**
- Each node contains: `id`, `tool_name`, `inputs`, `depends_on[]`
- Validates output against JSON Schema before passing to executor

**DAG Schema:**
```json
{
  "nodes": [
    {
      "id": "step_1",
      "tool": "web_search",
      "inputs": { "query": "..." },
      "depends_on": []
    },
    {
      "id": "step_2",
      "tool": "summarize",
      "inputs": { "text": "$step_1.output" },
      "depends_on": ["step_1"]
    }
  ]
}
```

#### Executor

- Topologically sorts DAG nodes
- Executes nodes **concurrently** where `depends_on` is satisfied
- Each node runs in isolation: timeout enforced, output validated against the registered tool's output schema
- Node state machine: `PENDING → RUNNING → SUCCESS | FAILED | RETRYING`

#### Tool Router

- Looks up tool by name in the Tool Registry (Postgres)
- Validates input against the tool's JSON Schema
- Dispatches call to the tool handler (`http endpoint` | `internal Go func` | `llm completion`)
- Returns typed output or structured error

#### Retry + Fallback

- Per-node retry count: configurable, default **2**
- Exponential backoff between retries
- On final failure: mark node `FAILED`, skip dependent nodes, emit trace event
- AI provider fallback: `OpenRouter 5xx → retry once → switch to direct Gemini API`

#### Guardrails

| Limit | Value | Enforcement |
|---|---|---|
| Max DAG depth | 10 nodes | Executor level |
| Max token budget | Configurable per agent | Executor level |
| Max recursion depth | 3 (for self-calling agents) | Executor level |
| Response filter | Strip PII patterns if enabled | Post-execution |

---

### 3.2 Prompt Versioning System

Treat prompts like code. Every change is a commit. Every version is **immutable**. Evaluation results are linked to the exact prompt version that produced them.

#### Version Model

- Each save creates a new immutable version (v1, v2, v3...)
- Previous versions are **read-only**
- Active version is a **pointer**, not a copy
- Rollback creates a new version entry (e.g. v4 = rollback to v2) for full audit trail

#### Prompt Version Schema

```json
{
  "prompt_id": "support_agent",
  "version": 3,
  "content": "...",
  "model": "claude-3-5-sonnet",
  "temperature": 0.2,
  "changes": "stricter hallucination avoidance",
  "created_at": "2026-03-23T...",
  "eval_run_ids": ["eval_22", "eval_23"]
}
```

#### Features

| Feature | Specification |
|---|---|
| Diff View | Frontend shows line-by-line unified diff between any two versions. Added lines in green, removed in red. |
| Eval Linking | Each eval run records `prompt_id + version`. Prompt history page shows eval scores per version as a performance graph over time. |
| Rollback | One-click rollback sets the active pointer to any prior `version_num`. Creates a new version entry for full audit trail. |

---

### 3.3 Evaluation Engine

The most critical module for demonstrating production readiness. Automated evaluation is what separates a toy agent from a deployable system.

#### Test Case Schema

```json
{
  "input": "What is your refund policy?",
  "expected_output": "We offer a 30-day full refund...",
  "criteria": "must include refund policy and mention 30 days"
}
```

#### Metric Specifications

| Metric | Method | Output |
|---|---|---|
| Exact Match | String equality after normalization (lowercase, strip punctuation) | `match: true/false` + normalized strings logged |
| Semantic Similarity | Embed both expected + actual. Cosine similarity on vectors. Threshold configurable (default `0.85`). | `score: 0.92, above_threshold: true` |
| LLM-as-Judge | Send expected + actual to Claude via OpenRouter. Structured prompt returns JSON with score (0–1) and reasoning. | `{ "score": 0.8, "reasoning": "Correct but verbose" }` |
| Custom Rules | Regex match, keyword presence/absence, JSON path assertions, length constraints. | `rule "must_include_refund": passed` |

#### Sample Eval Run Output

```
Agent: support_agent  |  Prompt: v3  |  Dataset: support_test_v2  |  Run: 2026-03-23
----------------------------------------
Test cases:          42
Exact match:         71.4%
Semantic similarity: 88.2%  (threshold: 0.85)
LLM-as-judge avg:    0.81 / 1.0
Custom rules pass:   95.2%
Avg latency:         1.8s  |  P95: 3.2s
Hallucination flags: 3 / 42  (7.1%)
OVERALL: PASS  (regression gate: >= 75% exact match)
```

---

### 3.4 Observability Layer

Every agent execution is fully traceable. Each step, tool call, token count, latency delta, and failure is recorded as a structured event and surfaced in the React trace viewer.

#### Trace Events (in order)

```
run_start → plan_generated → node_start → tool_called → tool_response → node_complete → node_failed → run_complete
```

Each event carries: `timestamp`, `duration_ms`, `node_id`, `tool_name`, `token_in`, `token_out`, `error (if any)`

#### Storage

- **Table:** `agent_traces` in Postgres
- Indexed by: `run_id`, `agent_id`, `created_at`
- Events stored as **JSONB array** per run for efficient retrieval

#### Frontend Trace UI

- **Timeline view:** horizontal swimlane per DAG node
- Color-coded by status: green (success) / red (failed) / yellow (retrying)
- Click any step → expand full inputs, outputs, token usage
- Collapsible tool call diffs

#### Metrics Dashboard (Recharts)

- Line charts: avg latency over time, token usage per run, error rate per agent
- Filterable by date range and agent version

#### Failure Handling

- In-app notification when a run reaches `FAILED` state
- V2: webhook integration for external alerting (Slack/Discord)

---

### 3.5 Memory System

#### Three-Tier Architecture

| Memory Type | Storage | Behaviour |
|---|---|---|
| Short-term (Session) | Redis | In-memory for the duration of a single agent run. Cleared on run completion. Holds intermediate DAG node outputs for `$ref` resolution between steps. |
| Long-term (Semantic) | Qdrant | Key facts and summaries embedded and stored. Retrieved by cosine similarity at run start. **Promotion rule:** if a session memory item scores > 0.9 relevance across 3+ runs, promote to long-term. |
| Episodic (Task History) | Postgres + Qdrant | Full run summaries stored as episodes. Agent can retrieve "what happened last time I did X" via semantic search. Enables learning from past executions. |

---

## 4. Database Schema (PostgreSQL)

### `agents`
```sql
id              UUID PRIMARY KEY
name            TEXT NOT NULL
description     TEXT
system_prompt_id UUID (FK → prompt_versions.prompt_id)
tool_ids        UUID[]
model_config    JSONB
active_version  INT
created_at      TIMESTAMPTZ DEFAULT now()
```

### `prompt_versions`
```sql
id              UUID PRIMARY KEY
prompt_id       TEXT NOT NULL         -- shared identifier across versions
version_num     INT NOT NULL
content         TEXT NOT NULL         -- immutable after insert
model           TEXT
temperature     FLOAT
changes         TEXT
created_at      TIMESTAMPTZ DEFAULT now()
eval_run_ids    UUID[]
UNIQUE(prompt_id, version_num)
```

### `tools`
```sql
id              UUID PRIMARY KEY
name            TEXT NOT NULL UNIQUE
description     TEXT
input_schema    JSONB NOT NULL
output_schema   JSONB NOT NULL
handler_type    TEXT CHECK (handler_type IN ('http', 'internal', 'llm'))
handler_config  JSONB
created_at      TIMESTAMPTZ DEFAULT now()
```

### `agent_runs`
```sql
id                UUID PRIMARY KEY
agent_id          UUID (FK → agents)
prompt_version_id UUID (FK → prompt_versions)
status            TEXT CHECK (status IN ('pending','running','success','failed'))
dag_plan          JSONB
goal              TEXT
started_at        TIMESTAMPTZ
completed_at      TIMESTAMPTZ
token_total       INT
```

### `agent_traces`
```sql
id          UUID PRIMARY KEY
run_id      UUID (FK → agent_runs)
events      JSONB     -- array of trace events, appended during execution
created_at  TIMESTAMPTZ DEFAULT now()
```

### `eval_datasets`
```sql
id          UUID PRIMARY KEY
name        TEXT NOT NULL
agent_id    UUID (FK → agents)
cases       JSONB     -- array of {input, expected_output, criteria}
created_at  TIMESTAMPTZ DEFAULT now()
```

### `eval_runs`
```sql
id                  UUID PRIMARY KEY
dataset_id          UUID (FK → eval_datasets)
agent_id            UUID (FK → agents)
prompt_version_id   UUID (FK → prompt_versions)
results             JSONB     -- per-case breakdown
exact_match_pct     FLOAT
semantic_avg        FLOAT
llm_judge_avg       FLOAT
custom_pass_pct     FLOAT
latency_avg_ms      INT
started_at          TIMESTAMPTZ
completed_at        TIMESTAMPTZ
```

### `memory_episodes`
```sql
id              UUID PRIMARY KEY
agent_id        UUID (FK → agents)
run_id          UUID (FK → agent_runs)
summary         TEXT
embedding_id    TEXT    -- reference to Qdrant point ID
created_at      TIMESTAMPTZ DEFAULT now()
```

---

## 5. REST API Surface (Go + Fiber)

**Base path:** `/api/v1`

**Response envelope:**
```json
{ "data": "...", "error": null }
```

All endpoints return `200` on success. Errors return `4xx/5xx` with:
```json
{ "data": null, "error": { "code": "...", "message": "..." } }
```

---

### Agents

| Method | Endpoint | Description |
|---|---|---|
| `GET` | `/agents` | List all agents with active version info |
| `POST` | `/agents` | Create agent. Body: `{ name, tool_ids, model_config }` |
| `GET` | `/agents/:id` | Get agent detail with tool list + prompt versions |
| `PUT` | `/agents/:id` | Update agent config |
| `DELETE` | `/agents/:id` | Soft-delete agent |
| `POST` | `/agents/:id/run` | Trigger agent run. Body: `{ goal, dataset_input? }`. Returns `run_id`. |
| `GET` | `/agents/:id/runs` | List all runs for an agent |

---

### Prompts

| Method | Endpoint | Description |
|---|---|---|
| `GET` | `/prompts/:id/versions` | List all versions for a prompt |
| `POST` | `/prompts/:id/versions` | Save new version. Body: `{ content, model, temperature, changes }`. Auto-increments `version_num`. |
| `GET` | `/prompts/:id/diff?v1=1&v2=3` | Return unified diff between two versions |
| `POST` | `/prompts/:id/rollback` | Body: `{ target_version }`. Sets active pointer. Creates new version entry. |
| `GET` | `/prompts/:id/versions/:version_num` | Get a specific version |

---

### Tools

| Method | Endpoint | Description |
|---|---|---|
| `GET` | `/tools` | List all registered tools |
| `POST` | `/tools` | Register tool. Body: `{ name, description, input_schema, output_schema, handler_type, handler_config }` |
| `GET` | `/tools/:id` | Get tool detail |
| `PUT` | `/tools/:id` | Update tool schema or config |
| `DELETE` | `/tools/:id` | Remove tool from registry |
| `POST` | `/tools/:id/test` | Execute tool with sample input for validation |

---

### Evaluations

| Method | Endpoint | Description |
|---|---|---|
| `POST` | `/evals/datasets` | Create eval dataset. Body: `{ name, agent_id, cases[] }` |
| `GET` | `/evals/datasets` | List all datasets |
| `GET` | `/evals/datasets/:id` | Get dataset + full case list |
| `PUT` | `/evals/datasets/:id` | Add/update test cases |
| `POST` | `/evals/run` | Trigger eval run. Body: `{ dataset_id, agent_id, prompt_version_id }` |
| `GET` | `/evals/runs/:id` | Get full eval run results + per-case breakdown |
| `GET` | `/evals/runs?agent_id=X` | List all eval runs for agent (for trend graph) |

---

### Traces & Runs

| Method | Endpoint | Description |
|---|---|---|
| `GET` | `/runs/:id/trace` | Full trace event log for a run |
| `GET` | `/runs/:id/status` | Lightweight poll: `{ status, completed_pct, node_states }` |
| `GET` | `/runs` | List recent runs across all agents |

---

## 6. CI/CD Pipeline

Every pull request is a quality gate. The **eval regression gate** is the headline feature — it enforces that no prompt or agent change silently degrades performance.

### Pipeline Stages

| Stage | Trigger | What Happens |
|---|---|---|
| Unit Tests | Every PR | `go test ./...` covering tool validation, DAG parser, schema enforcement, scoring functions. Must pass 100%. |
| Eval Regression Gate | Every PR | Runs eval dataset against PR's agent/prompt changes. Compares `exact_match_pct` to `main` branch baseline. **Fails PR if score drops > 3%.** This is the key differentiator. |
| Docker Build | Every PR | `docker build` for backend + frontend. Ensures build is not broken. Does not push. |
| Docker Push | Merge to `main` | Builds + pushes tagged image to GHCR. Tag: `git SHA` + `latest`. |
| Deploy to VM | Manual trigger (v1) | SSH to cloud VM, `docker-compose pull && docker-compose up -d`. Automated CD deferred to v2. |

### GitHub Actions Workflow

```yaml
# .github/workflows/ci.yml
name: CI

on: [pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.22' }
      - run: go test ./...

  eval-gate:
    needs: test
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Run eval regression
        run: go run ./cmd/eval --dataset smoke_test --baseline main --fail-below 3
        env:
          OPENROUTER_API_KEY: ${{ secrets.OPENROUTER_API_KEY }}
          DATABASE_URL: ${{ secrets.DATABASE_URL }}

  docker-build:
    needs: eval-gate
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: docker compose build

# .github/workflows/cd.yml
name: CD

on:
  push:
    branches: [main]

jobs:
  docker-push:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Login to GHCR
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - name: Build and push
        run: |
          docker compose build
          docker compose push
```

---

## 7. Repository Structure

```
agentops/
├── backend/                        # Go service
│   ├── cmd/
│   │   ├── api/                    # API server entrypoint (main.go)
│   │   └── eval/                   # Eval CLI entrypoint (used in CI gate)
│   ├── internal/
│   │   ├── agent/                  # Runtime: planner, executor, tool router
│   │   │   ├── planner.go          # LLM → DAG JSON generation
│   │   │   ├── executor.go         # Topological sort + concurrent node execution
│   │   │   ├── router.go           # Tool dispatch
│   │   │   └── guardrails.go       # Limits enforcement
│   │   ├── prompt/                 # Versioning, diff, rollback
│   │   │   ├── store.go
│   │   │   └── diff.go
│   │   ├── eval/                   # Scoring pipeline
│   │   │   ├── exact.go
│   │   │   ├── semantic.go         # Embedding + cosine similarity
│   │   │   ├── judge.go            # LLM-as-judge
│   │   │   └── rules.go            # Custom rule engine
│   │   ├── memory/
│   │   │   ├── session.go          # Redis short-term
│   │   │   ├── longterm.go         # Qdrant semantic memory
│   │   │   └── episodic.go         # Run summaries + promotion logic
│   │   ├── tools/
│   │   │   ├── registry.go         # Load tools from Postgres
│   │   │   ├── validator.go        # JSON Schema input/output validation
│   │   │   └── handler.go          # http | internal | llm dispatch
│   │   ├── trace/
│   │   │   ├── emitter.go          # Event emission during runs
│   │   │   └── store.go            # Persist to agent_traces table
│   │   └── db/
│   │       ├── postgres.go
│   │       ├── qdrant.go
│   │       └── redis.go
│   ├── api/                        # Fiber route handlers
│   │   ├── agents.go
│   │   ├── prompts.go
│   │   ├── tools.go
│   │   ├── evals.go
│   │   └── traces.go
│   ├── migrations/                 # SQL migration files
│   └── Dockerfile
├── frontend/                       # React + Vite + shadcn/ui
│   ├── src/
│   │   ├── pages/
│   │   │   ├── Agents.tsx          # Agent list + builder
│   │   │   ├── PromptStudio.tsx    # Version history + diff viewer
│   │   │   ├── EvalDashboard.tsx   # Scores, trends, per-case breakdown
│   │   │   └── TraceViewer.tsx     # Swimlane timeline view
│   │   ├── components/
│   │   │   ├── DAGViewer.tsx       # Visual DAG representation
│   │   │   ├── DiffViewer.tsx      # Prompt version diff
│   │   │   ├── EvalChart.tsx       # Recharts eval trend lines
│   │   │   └── TraceTimeline.tsx   # Horizontal swimlane per node
│   │   └── api/
│   │       └── client.ts           # Typed fetch wrappers for all endpoints
│   └── Dockerfile
├── infra/
│   ├── docker-compose.yml          # postgres, qdrant, redis, backend, frontend, nginx
│   └── nginx.conf                  # Reverse proxy for VM deploy
├── eval-data/
│   └── smoke_test.json             # Baseline eval dataset used in CI gate
├── .github/
│   └── workflows/
│       ├── ci.yml                  # PR: test + eval gate + docker build
│       └── cd.yml                  # Merge to main: docker push to GHCR
└── README.md
```

### Docker Compose Services

```yaml
# infra/docker-compose.yml (sketch)
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_DB: agentops
      POSTGRES_USER: agentops
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
    volumes: [postgres_data:/var/lib/postgresql/data]
    ports: ["5432:5432"]

  qdrant:
    image: qdrant/qdrant:latest
    ports: ["6333:6333"]
    volumes: [qdrant_data:/qdrant/storage]

  redis:
    image: redis:7-alpine
    ports: ["6379:6379"]

  backend:
    build: ./backend
    environment:
      DATABASE_URL: postgres://agentops:${POSTGRES_PASSWORD}@postgres:5432/agentops
      QDRANT_URL: http://qdrant:6333
      REDIS_URL: redis://redis:6379
      OPENROUTER_API_KEY: ${OPENROUTER_API_KEY}
      GEMINI_API_KEY: ${GEMINI_API_KEY}
    ports: ["8080:8080"]
    depends_on: [postgres, qdrant, redis]

  frontend:
    build: ./frontend
    ports: ["3000:3000"]
    depends_on: [backend]

  nginx:
    image: nginx:alpine
    ports: ["80:80", "443:443"]
    volumes: [./infra/nginx.conf:/etc/nginx/nginx.conf]
    depends_on: [backend, frontend]
```

---

## 8. Build Phases & Milestones

Ship in layers. Each phase produces a **working system**, not a half-built one.

### Phase 1 — Foundation
**Deliverables:**
- Docker Compose up with all services running
- Postgres migrations applied (all 7 tables)
- Go API boots, health check returns 200
- Agent CRUD (`POST /agents`, `GET /agents`, `GET /agents/:id`)

**Definition of Done:** Can create an agent via REST, store it in Postgres, retrieve it. All containers start cleanly from `docker-compose up`.

---

### Phase 2 — Runtime Core
**Deliverables:**
- Planner: LLM call → validated DAG JSON
- Executor: topological sort + concurrent node execution
- Tool registry loaded from Postgres
- Tool Router: validates input, dispatches call, validates output
- Retry + fallback logic
- Basic trace events written to `agent_traces`
- `POST /agents/:id/run` endpoint live

**Definition of Done:** Can run an agent with 2+ tools, see the DAG executed step by step, trace stored in Postgres. Intentional tool failure triggers retry then FAILED state.

---

### Phase 3 — Evaluation Engine
**Deliverables:**
- Eval dataset CRUD (`POST /evals/datasets`, `GET /evals/datasets/:id`)
- `POST /evals/run` triggers full scoring pipeline
- All 4 metrics implemented: exact match, semantic (Qdrant embeddings), LLM-as-judge, custom rules
- Results stored in `eval_runs` table
- `GET /evals/runs/:id` returns full per-case breakdown

**Definition of Done:** Can create 5 test cases, trigger eval run, get back a scored report. LLM judge returns JSON with score + reasoning. Semantic scorer returns cosine value.

---

### Phase 4 — Prompt Versioning
**Deliverables:**
- `POST /prompts/:id/versions` creates immutable version
- `GET /prompts/:id/diff?v1=1&v2=3` returns unified diff
- `POST /prompts/:id/rollback` updates active pointer
- Eval runs linked to prompt version at time of run

**Definition of Done:** Can save 3 prompt versions, view diff between v1 and v3, rollback to v2, see which eval run used which version. Older versions remain readable and unchanged.

---

### Phase 5 — Memory System
**Deliverables:**
- Session memory in Redis (key: `session:{run_id}:{node_id}`)
- Long-term memory write + retrieval via Qdrant
- Episodic summary generation at run completion
- Memory promotion logic (0.9 score threshold × 3 runs → long-term)

**Definition of Done:** Agent recalls a fact from a previous run via semantic retrieval. Episodic summary stored in Qdrant after run. Promoted memories persist across restarts.

---

### Phase 6 — Frontend
**Deliverables:**
- **Agent Builder:** create/edit agents, assign tools, set model config
- **Prompt Studio:** version history list, diff viewer between any two versions, rollback button
- **Eval Dashboard:** run eval, view per-metric scores, trend line chart over prompt versions (Recharts)
- **Trace Viewer:** horizontal swimlane per DAG node, color-coded status, click-to-expand step detail

**Definition of Done:** Full user journey completable in browser: create agent → define tools → run → view trace → run eval → update prompt → see score improve.

---

### Phase 7 — CI/CD + Polish
**Deliverables:**
- GitHub Actions CI fully wired (unit tests + eval gate + docker build on every PR)
- GitHub Actions CD (docker push to GHCR on merge to main)
- `eval-data/smoke_test.json` baseline dataset committed to repo
- VM deployment tested end-to-end with nginx reverse proxy
- README with architecture diagram + quickstart

**Definition of Done:** PR that drops eval accuracy by >3% fails CI check. Merge to main triggers Docker push. `docker-compose up` on a fresh VM boots the full stack. README lets a new developer run the project in under 10 minutes.

---

## 9. The Iteration Loop (SDLC Core)

```
Deploy Agent
    ↓
Run Evals (against test dataset)
    ↓
Analyze Traces (find failing steps, token waste, latency spikes)
    ↓
Update Prompt (creates new immutable version)
    ↓
Re-run Evals (compare against previous version score)
    ↓
CI Eval Gate (fail PR if score regresses > 3%)
    ↓
Merge to main
    ↓
Docker Push → Deploy
    ↓
(repeat)
```

> Every step is traceable. Every decision is versioned. Every regression is caught before it ships.

### What Makes This Production-Grade

| Property | How AgentOps Achieves It |
|---|---|
| Determinism | DAG execution is topologically ordered. Same plan = same execution order, every time. |
| Observability | Every token, every tool call, every step latency is stored. Nothing is a black box. |
| Quality Enforcement | Eval regression gate in CI means accuracy is a first-class build artifact, not a post-deploy concern. |
| Versioning | Prompts and agents are versioned like code. Any state can be reproduced exactly. |
| Fault Isolation | Tool failures are per-node. One bad tool call does not crash the run. Retry + fallback is automatic. |
| Provider Resilience | OpenRouter → Gemini fallback means the system survives AI provider outages. |
| Memory Architecture | Three-tier memory (session / long-term / episodic) mirrors how real production agents work. |

---

## 10. V2 Backlog (Deferred, Not Forgotten)

| Feature | Notes |
|---|---|
| WebSocket live traces | Stream trace events to frontend in real time during agent execution. REST polling is v1. WS is a polish feature. |
| Kubernetes deployment | Helm chart for K8s. Big bonus signal on a portfolio. Not needed for functional v1. |
| Multi-tenancy | User accounts, API keys, agent isolation per user. Requires full auth layer redesign. |
| Automated CD | SSH deploy on merge to main. Currently manual trigger. Needs secrets management. |
| Integration tests | Full agent run tests in CI. Expensive (hits real AI APIs). Requires test-mode provider stub first. |
| Webhook alerts | POST to Slack/Discord on agent run failure or eval regression. |
| Prompt A/B testing | Split traffic between two prompt versions, compare eval scores live. |
| Tool marketplace | Pre-built tool templates (web search, code exec, email, calendar) importable from UI. |

---

*AgentOps — Technical Planning Document — v1.0 — March 2026*
*All decisions locked. Build Phase 1 begins.*
