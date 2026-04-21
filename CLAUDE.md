# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build and Test Commands

```bash
make build          # Compile binary to ./bin/llm-gateway
make run            # Build and run with config.yaml
make test           # Run tests with race detection
make vet            # Run go vet
make docker-build   # Build Docker image
make docker-up      # Start via docker compose
make docker-down    # Stop via docker compose
```

For local development without Docker:
```bash
make build
PROVIDER=jdcloud CONFIG_FILE=config.yaml ./bin/llm-gateway
```

## Architecture

This is a Go reverse proxy for Anthropic/OpenAI-compatible APIs with automatic retry on overload and token usage statistics.

### Request Flow
```
Client → proxy.Handler → upstream API
              ↓
         overload detected?
              ↓
      retry with linear backoff (delay + N × jitter)
              ↓
         stats.DB (async token recording)
```

### Key Packages

- **cmd/llm-gateway**: Entry point, loads config, initializes stats DB, starts HTTP server
- **internal/config**: YAML config loading with env var overrides (PROVIDER, UPSTREAM_URL, STATS_DB, PORT)
- **internal/proxy**: Core reverse proxy handler with overload retry logic
- **internal/provider**: Overload rule matching (status code + optional body substring)
- **internal/stats**: SQLite-backed token usage recording, HTTP endpoints for dashboard

### Protocol Support

Two parsers in `internal/stats/`:
- **anthropic.go**: Anthropic Messages API format (default)
- **openai.go**: OpenAI Chat Completions format (requires `stream_options.include_usage: true` for streaming)

### Configuration

- `config.yaml` defines providers with upstream URLs and overload rules
- Environment variables override file settings at runtime
- Each overload rule specifies: `status`, optional `body_contains`, `max_retries`, `delay`, `jitter`

### Stats Endpoints

- `/stats` — HTML dashboard (hand-drawn style)
- `/stats/data` — JSON API with summary, by_day, by_model aggregations
