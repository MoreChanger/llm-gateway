# 多协议自动路由设计

**日期：** 2026-04-21
**状态：** 设计确认，待实现

## 目标

让一个代理实例同时支持 Anthropic 和 OpenAI 协议，根据请求路径自动识别协议并路由到对应上游。

## 使用场景

```
Claude Code（Anthropic）     Cursor（OpenAI）
        ↓                          ↓
        └──────── 127.0.0.1:8088 ──┘
                    ↓
              自动识别协议
                    ↓
    ┌───────────────┴───────────────┐
    ↓                               ↓
Anthropic 上游                  OpenAI 上游
```

客户端只需配置代理地址，协议识别完全自动。

## 配置格式

```yaml
listen: :8080
stats_db: ./data/stats.db

# 上游定义
upstreams:
  anthropic:
    url: https://modelservice.jdcloud.com/coding/anthropic
    protocol: anthropic    # 可选，默认 anthropic
  openai:
    url: https://modelservice.jdcloud.com/coding/openai/v1
    protocol: openai

# 路由规则
routes:
  - path: /v1/messages
    upstream: anthropic
  - path: /v1/chat/completions
    upstream: openai

# 全局重试规则
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

## 路由逻辑

```
请求进入 → 提取 URL.Path
              ↓
         遍历 routes 列表
              ↓
         精确匹配 path？
         ├─ 是 → 选择对应 upstream + parser
         └─ 否 → 返回 404 {"error": "unsupported path", "path": "/xxx"}
```

**匹配规则：**
- 精确匹配路径
- 按配置顺序匹配
- 未匹配返回 JSON 错误

## 向后兼容

保留旧配置格式支持：

```yaml
# 旧格式（仍然支持）
active: jdcloud
providers:
  jdcloud:
    upstream: https://xxx
    overload_rules: [...]
```

**兼容逻辑：**
- 检测到 `providers` 字段 → 使用旧模式（单 Provider）
- 检测到 `upstreams` + `routes` → 使用新模式（多路由）
- 两者都没有 → 配置错误

## 代码变更

| 文件 | 变更内容 |
|------|----------|
| `internal/config/config.go` | 新增 Upstream、Route 结构体，加载多上游配置 |
| `internal/proxy/proxy.go` | 路径路由逻辑，动态选择 upstream 和 parser |
| `config.yaml` | 配置格式变更 |
| `README.md` | 文档更新 |

## 行为变化

### Token 统计
- 新模式：每个路由使用对应协议的 Parser 解析响应

### 日志输出
- 新增路由信息：`route=/v1/messages upstream=anthropic`
- 未匹配路径：`path=/xxx error="unsupported path"`

### 错误响应
- 未匹配路径返回：
  ```json
  {"error": "unsupported path", "path": "/v1/unknown"}
  ```

## 成功标准

1. Claude Code 通过代理正常调用 Anthropic API
2. OpenAI 客户端通过代理正常调用 OpenAI API
3. 两种客户端可同时连接同一代理实例
4. Token 统计正确记录两种协议的用量
5. 旧配置格式继续正常工作
