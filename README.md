# llm-gateway

支持 **Anthropic 协议**与 **OpenAI 协议**的轻量级反向代理。在上游过载时自动等待并重试，确保 Claude Code 等客户端不因接口报错而中断任务；同时异步统计 token 用量并提供可视化看板。

适用场景：京东云、百度、自建中转等各类 Anthropic / OpenAI 兼容 API。

## 工作原理

```
Claude Code  ──►  localhost:8087  ──►  上游 Anthropic / OpenAI 兼容 API
               (llm-gateway)
                      │
                      │ 收到过载错误（可配置）
                      │ 自动等待 + 重试（线性退避）
                      └──────────────────────────►  重新转发
```

重试间隔：第 N 次等待 `delay + N × jitter`，例如默认配置下为 2s、3s、4s……

## 快速开始

### 使用 Docker（推荐）

**1. 检查 `.env`**

```bash
PORT=8087          # 宿主机监听端口
```

**2. 启动**

```bash
docker compose up -d
```

代理监听在 `127.0.0.1:8087`，在 Claude Code 中配置 `ANTHROPIC_BASE_URL=http://127.0.0.1:8087` 即可。

---

### 本地运行（不使用 Docker）

需要先安装 [Go 1.25+](https://go.dev/dl/)。

```bash
make build
CONFIG_FILE=config.yaml ./bin/llm-gateway
```

---

## 多协议自动路由（推荐）

一个代理实例同时支持 Anthropic 和 OpenAI 协议，根据请求路径自动识别并路由。

```
Claude Code（Anthropic）     Cursor（OpenAI）
        ↓                          ↓
        └──────── 127.0.0.1:8087 ──┘
                    ↓
              自动识别协议
                    ↓
    ┌───────────────┴───────────────┐
    ↓                               ↓
Anthropic 上游                  OpenAI 上游
```

**配置示例：**

```yaml
upstreams:
  anthropic:
    url: https://modelservice.jdcloud.com/coding/anthropic
    protocol: anthropic
  openai:
    url: https://modelservice.jdcloud.com/coding/openai
    protocol: openai

routes:
  - path: /v1/messages
    upstream: anthropic
  - path: /v1/chat/completions
    upstream: openai

overload_rules:
  - status: 400
    body_contains: "overloaded"
    max_retries: 10
    delay: 2s
    jitter: 1s
  - status: 429
    max_retries: 10
    delay: 2s
    jitter: 1s
  - status: 500
    max_retries: 5
    delay: 3s
    jitter: 1s
```

客户端只需配置代理地址 `http://127.0.0.1:8087`，协议识别完全自动。

---

## Token 用量统计

代理会异步捕获每次请求的响应，解析 Anthropic / OpenAI 协议中的 token 用量，写入本地 SQLite 数据库。

| 地址 | 说明 |
|---|---|
| `http://127.0.0.1:<PORT>/stats` | 可视化看板（手绘风格 Dashboard） |
| `http://127.0.0.1:<PORT>/stats/data` | JSON 数据接口 |

看板包含：总请求数、输入/输出/合计 token 数、近 30 天每日用量图、按模型分布表。

统计数据库默认保存在 `./data/stats.db`，Docker 模式下通过 volume 持久化。

---

## 配置说明

### 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `PORT` | `8087` | 宿主机监听端口 |
| `STATS_DB` | — | 覆盖统计数据库路径，留空则使用 `config.yaml` 中的 `stats_db` |

### config.yaml 完整结构

```yaml
listen: :8080              # 容器内监听地址，无需修改
stats_db: ./data/stats.db  # SQLite 路径，留空禁用统计

upstreams:
  anthropic:
    url: https://modelservice.jdcloud.com/coding/anthropic
    protocol: anthropic    # 可选，默认 anthropic
  openai:
    url: https://modelservice.jdcloud.com/coding/openai
    protocol: openai

routes:
  - path: /v1/messages
    upstream: anthropic
  - path: /v1/chat/completions
    upstream: openai

overload_rules:
  - status: 400
    body_contains: "overloaded"
    max_retries: 10   # 省略则使用默认值 10
    delay: 2s         # 省略则使用默认值 2s
    jitter: 1s        # 省略则使用默认值 1s
  - status: 429
    max_retries: 10
    delay: 2s
    jitter: 1s
```

### 支持的 protocol

| 值 | 适用场景 |
|---|---|
| `anthropic`（默认）| 京东云、官方 Anthropic 及所有 Anthropic 兼容接口 |
| `openai` | OpenAI Chat Completions 格式的接口（流式统计需请求带 `stream_options.include_usage: true`） |

---

## 旧版配置格式（仍然支持）

如果只需要单个上游，可以使用旧版配置格式：

```yaml
listen: :8080
active: jdcloud
stats_db: ./data/stats.db

providers:
  jdcloud:
    upstream: https://modelservice.jdcloud.com/coding/anthropic
    overload_rules:
      - status: 400
        body_contains: "overloaded"
        max_retries: 10
        delay: 2s
        jitter: 1s
```

环境变量 `PROVIDER` 可覆盖 `active` 字段。
