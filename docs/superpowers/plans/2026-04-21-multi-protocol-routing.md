# 多协议自动路由实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让代理根据请求路径自动识别 Anthropic/OpenAI 协议并路由到对应上游

**Architecture:** 在配置层新增 upstreams 和 routes 结构，代理层根据路径匹配选择上游和 parser，保持向后兼容旧配置格式

**Tech Stack:** Go 1.25, yaml.v3, 现有 parser 抽象

---

## 文件结构

| 文件 | 职责 |
|------|------|
| `internal/config/config.go` | 配置加载，新增 Upstream/Route 结构，新旧格式兼容 |
| `internal/proxy/proxy.go` | 代理逻辑，路径路由，动态选择 upstream/parser |
| `cmd/anthropic-proxy/main.go` | 入口，支持新模式初始化 |
| `config.yaml` | 配置示例，新格式 |
| `README.md` | 文档更新 |

---

### Task 1: 新增配置结构体

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: 添加 Upstream 和 Route 结构体**

在 `config.go` 中，在 `Config` 结构体之后添加：

```go
// Upstream defines an upstream API endpoint.
type Upstream struct {
	URL      string // Upstream API URL
	Protocol string // "anthropic" (default) or "openai"
}

// Route maps a request path to an upstream.
type Route struct {
	Path     string // Request path to match (exact match)
	Upstream string // Name of the upstream to use
}

// MultiConfig is the runtime configuration for multi-protocol routing mode.
type MultiConfig struct {
	ListenAddr    string
	Upstreams     map[string]Upstream // upstream name -> Upstream
	Routes        []Route
	OverloadRules []provider.Rule
	StatsDB       string
}
```

- [ ] **Step 2: 添加 YAML 映射结构体**

在 `Route` 结构体之后添加：

```go
type upstreamYAML struct {
	URL      string `yaml:"url"`
	Protocol string `yaml:"protocol"`
}

type routeYAML struct {
	Path     string `yaml:"path"`
	Upstream string `yaml:"upstream"`
}

type multiFileConfig struct {
	Listen        string                  `yaml:"listen"`
	StatsDB       string                  `yaml:"stats_db"`
	Upstreams     map[string]upstreamYAML `yaml:"upstreams"`
	Routes        []routeYAML             `yaml:"routes"`
	OverloadRules []ruleYAML              `yaml:"overload_rules"`
}
```

- [ ] **Step 3: Commit**

```bash
git add internal/config/config.go
git commit -m "feat(config): add Upstream, Route, MultiConfig structures for multi-protocol routing"
```

---

### Task 2: 实现配置加载逻辑

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: 添加 LoadMulti 函数**

在 `Load` 函数之后添加：

```go
// LoadMulti reads a multi-protocol routing config file.
// Returns nil if the file uses the old single-provider format.
func LoadMulti(path string) (*MultiConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	// First, detect config format
	var fc fileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	// Old format: has providers field
	if len(fc.Providers) > 0 {
		return nil, nil // Not a multi-config
	}

	// Parse as multi-config
	var mfc multiFileConfig
	if err := yaml.Unmarshal(data, &mfc); err != nil {
		return nil, fmt.Errorf("parse multi-config file: %w", err)
	}

	return resolveMulti(mfc)
}

func resolveMulti(mfc multiFileConfig) (*MultiConfig, error) {
	if len(mfc.Upstreams) == 0 {
		return nil, fmt.Errorf("config: upstreams must not be empty")
	}
	if len(mfc.Routes) == 0 {
		return nil, fmt.Errorf("config: routes must not be empty")
	}

	listen := mfc.Listen
	if listen == "" {
		listen = defaultListenAddr
	}

	// Resolve upstreams
	upstreams := make(map[string]Upstream)
	for name, u := range mfc.Upstreams {
		if u.URL == "" {
			return nil, fmt.Errorf("upstream %q: url is required", name)
		}
		protocol := u.Protocol
		if protocol == "" {
			protocol = "anthropic"
		}
		upstreams[name] = Upstream{
			URL:      strings.TrimRight(u.URL, "/"),
			Protocol: protocol,
		}
	}

	// Validate routes reference valid upstreams
	for _, r := range mfc.Routes {
		if r.Path == "" {
			return nil, fmt.Errorf("route: path is required")
		}
		if r.Upstream == "" {
			return nil, fmt.Errorf("route for path %q: upstream is required", r.Path)
		}
		if _, ok := upstreams[r.Upstream]; !ok {
			return nil, fmt.Errorf("route for path %q: upstream %q not found", r.Path, r.Upstream)
		}
	}

	// Resolve overload rules
	rules := resolveRules(mfc.OverloadRules)

	statsDB := mfc.StatsDB
	if v := os.Getenv("STATS_DB"); v != "" {
		statsDB = v
	}

	return &MultiConfig{
		ListenAddr:    listen,
		Upstreams:     upstreams,
		Routes:        resolveRoutes(mfc.Routes),
		OverloadRules: rules,
		StatsDB:       statsDB,
	}, nil
}

func resolveRules(rulesYAML []ruleYAML) []provider.Rule {
	if len(rulesYAML) == 0 {
		return nil
	}
	rules := make([]provider.Rule, len(rulesYAML))
	for i, r := range rulesYAML {
		rule := provider.Rule{
			Status:       r.Status,
			BodyContains: r.BodyContains,
			MaxRetries:   defaultMaxRetries,
			RetryDelay:   defaultRetryDelay,
			RetryJitter:  defaultRetryJitter,
		}
		if r.MaxRetries != nil {
			rule.MaxRetries = *r.MaxRetries
		}
		if r.Delay != nil {
			rule.RetryDelay = r.Delay.Duration
		}
		if r.Jitter != nil {
			rule.RetryJitter = r.Jitter.Duration
		}
		rules[i] = rule
	}
	return rules
}

func resolveRoutes(routesYAML []routeYAML) []Route {
	routes := make([]Route, len(routesYAML))
	for i, r := range routesYAML {
		routes[i] = Route{Path: r.Path, Upstream: r.Upstream}
	}
	return routes
}
```

- [ ] **Step 2: 添加 IsMultiConfig 检测函数**

在 `LoadMulti` 函数之前添加：

```go
// IsMultiConfig checks if the config file uses multi-protocol routing format.
func IsMultiConfig(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read config file: %w", err)
	}
	var fc fileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return false, fmt.Errorf("parse config file: %w", err)
	}
	// Old format has providers, new format has upstreams/routes
	return len(fc.Providers) == 0, nil
}
```

- [ ] **Step 3: Commit**

```bash
git add internal/config/config.go
git commit -m "feat(config): add LoadMulti for multi-protocol routing config loading"
```

---

### Task 3: 实现多路由代理逻辑

**Files:**
- Modify: `internal/proxy/proxy.go`

- [ ] **Step 1: 添加 NewMulti 函数**

在 `New` 函数之后添加：

```go
// NewMulti returns an http.Handler that routes requests based on path,
// automatically selecting the appropriate upstream and parser.
func NewMulti(cfg *config.MultiConfig, client *http.Client, sdb *stats.DB) http.Handler {
	return &multiHandler{
		cfg:    cfg,
		client: client,
		stats:  sdb,
	}
}

type multiHandler struct {
	cfg    *config.MultiConfig
	client *http.Client
	stats  *stats.DB
}
```

- [ ] **Step 2: 实现 multiHandler.ServeHTTP**

在 `multiHandler` 结构体之后添加：

```go
func (h *multiHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Find matching route
	var upstream *config.Upstream
	var upstreamName string
	var parser stats.Parser

	for _, route := range h.cfg.Routes {
		if r.URL.Path == route.Path || strings.HasPrefix(r.URL.Path, route.Path+"/") {
			u, ok := h.cfg.Upstreams[route.Upstream]
			if !ok {
				http.Error(w, fmt.Sprintf(`{"error":"upstream %q not found"}`, route.Upstream), http.StatusInternalServerError)
				return
			}
			upstream = &u
			upstreamName = route.Upstream
			parser = stats.NewParser(u.Protocol)
			break
		}
	}

	if upstream == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error":"unsupported path","path":%q}`, r.URL.Path)
		return
	}

	target := upstream.URL + r.RequestURI
	start := time.Now()

	slog.Info("->", "method", r.Method, "path", r.URL.Path, "route", upstreamName)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusInternalServerError)
		return
	}
	r.Body.Close()

	var rule *provider.Rule

	for attempt := 0; ; attempt++ {
		if rule != nil {
			if attempt > rule.MaxRetries {
				slog.Warn("max retries reached, giving up", "upstream", upstreamName, "max", rule.MaxRetries)
				break
			}
			wait := rule.RetryDelay + time.Duration(attempt)*rule.RetryJitter
			slog.Info("retry", "upstream", upstreamName, "attempt", attempt, "max", rule.MaxRetries, "wait", wait, "path", r.URL.Path)

			select {
			case <-r.Context().Done():
				http.Error(w, "client disconnected", http.StatusGatewayTimeout)
				return
			case <-time.After(wait):
			}
		}

		resp, err := h.do(r.Context(), r.Method, target, r.Header, body)
		if err != nil {
			if rule != nil && attempt >= rule.MaxRetries {
				slog.Error("upstream failed", "upstream", upstreamName, "attempts", attempt+1, "err", err)
				http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
				return
			}
			slog.Warn("upstream error, will retry", "upstream", upstreamName, "attempt", attempt+1, "err", err)
			if rule == nil && len(h.cfg.OverloadRules) > 0 {
				rule = &h.cfg.OverloadRules[0]
			}
			continue
		}

		if resp.StatusCode < 400 {
			slog.Info("<-", "status", resp.StatusCode, "path", r.URL.Path, "upstream", upstreamName, "attempts", attempt+1, "elapsed", time.Since(start).Round(time.Millisecond))
			captured := stream(w, resp)
			if h.stats != nil {
				h.stats.RecordAsync(upstreamName, r.URL.Path, captured, parser)
			}
			return
		}

		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if matched := provider.Match(h.cfg.OverloadRules, resp.StatusCode, errBody); matched != nil {
			if rule == nil {
				rule = matched
			}
			continue
		}

		forward(w, resp, errBody)
		return
	}

	// Final attempt after max retries
	resp, err := h.do(r.Context(), r.Method, target, r.Header, body)
	if err != nil {
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	errBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	forward(w, resp, errBody)
}

func (h *multiHandler) do(ctx context.Context, method, url string, headers http.Header, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return h.client.Do(req)
}
```

- [ ] **Step 3: 添加必要的 import**

确保 `proxy.go` 顶部 import 包含 `fmt`：

```go
import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"anthropic-proxy/internal/config"
	"anthropic-proxy/internal/provider"
	"anthropic-proxy/internal/stats"
)
```

- [ ] **Step 4: Commit**

```bash
git add internal/proxy/proxy.go
git commit -m "feat(proxy): add multiHandler for path-based routing"
```

---

### Task 4: 更新主入口支持新模式

**Files:**
- Modify: `cmd/anthropic-proxy/main.go`

- [ ] **Step 1: 修改 main 函数支持两种配置模式**

将 `main` 函数中的配置加载逻辑改为：

```go
func main() {
	configFile := flag.String("config", os.Getenv("CONFIG_FILE"), "YAML config file path")
	flag.Parse()

	if *configFile == "" {
		slog.Error("config file is required: use -config <path> or set CONFIG_FILE env var")
		os.Exit(1)
	}

	// Try multi-config first
	multiCfg, err := config.LoadMulti(*configFile)
	if err != nil {
		slog.Error("configuration error", "err", err)
		os.Exit(1)
	}

	if multiCfg != nil {
		runMulti(multiCfg)
		return
	}

	// Fall back to single-provider config
	cfg, err := config.Load(*configFile)
	if err != nil {
		slog.Error("configuration error", "err", err)
		os.Exit(1)
	}

	runSingle(cfg)
}
```

- [ ] **Step 2: 添加 runSingle 和 runMulti 函数**

在 `main` 函数之后添加：

```go
func runSingle(cfg *config.Config) {
	slog.Info("anthropic-proxy starting (single-provider mode)",
		"provider", cfg.ProviderName,
		"listen", cfg.ListenAddr,
		"upstream", cfg.Upstream,
		"overload_rules", fmtRules(cfg))

	var sdb *stats.DB
	var err error
	if cfg.StatsDB != "" {
		sdb, err = stats.Open(cfg.StatsDB)
		if err != nil {
			slog.Error("stats: failed to open db", "path", cfg.StatsDB, "err", err)
			os.Exit(1)
		}
		defer sdb.Close()
		slog.Info("stats enabled", "db", cfg.StatsDB, "endpoint", "/stats")
	}

	client := &http.Client{Timeout: 10 * time.Minute}
	mux := http.NewServeMux()
	if sdb != nil {
		mux.HandleFunc("/stats/data", sdb.Handler())
		mux.HandleFunc("/stats", sdb.UIHandler())
	}
	mux.Handle("/", proxy.New(cfg, client, sdb))

	if err := http.ListenAndServe(cfg.ListenAddr, mux); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func runMulti(cfg *config.MultiConfig) {
	slog.Info("anthropic-proxy starting (multi-protocol mode)",
		"listen", cfg.ListenAddr,
		"upstreams", len(cfg.Upstreams),
		"routes", len(cfg.Routes))

	var sdb *stats.DB
	var err error
	if cfg.StatsDB != "" {
		sdb, err = stats.Open(cfg.StatsDB)
		if err != nil {
			slog.Error("stats: failed to open db", "path", cfg.StatsDB, "err", err)
			os.Exit(1)
		}
		defer sdb.Close()
		slog.Info("stats enabled", "db", cfg.StatsDB, "endpoint", "/stats")
	}

	client := &http.Client{Timeout: 10 * time.Minute}
	mux := http.NewServeMux()
	if sdb != nil {
		mux.HandleFunc("/stats/data", sdb.Handler())
		mux.HandleFunc("/stats", sdb.UIHandler())
	}
	mux.Handle("/", proxy.NewMulti(cfg, client, sdb))

	if err := http.ListenAndServe(cfg.ListenAddr, mux); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 3: Commit**

```bash
git add cmd/anthropic-proxy/main.go
git commit -m "feat(main): support both single-provider and multi-protocol modes"
```

---

### Task 5: 更新配置文件示例

**Files:**
- Modify: `config.yaml`

- [ ] **Step 1: 添加新格式配置示例**

将 `config.yaml` 更新为包含两种格式的示例（旧格式注释保留）：

```yaml
listen: :8080
stats_db: ./data/stats.db

# ============================================================
# 新格式：多协议自动路由（推荐）
# ============================================================
upstreams:
  anthropic:
    url: https://modelservice.jdcloud.com/coding/anthropic
    protocol: anthropic
  openai:
    url: https://modelservice.jdcloud.com/coding/openai/v1
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
  - status: 400
    body_contains: "Too many requests"
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

# ============================================================
# 旧格式：单 Provider（仍然支持，与新格式互斥）
# ============================================================
# active: jdcloud
# providers:
#   jdcloud:
#     upstream: https://modelservice.jdcloud.com/coding/anthropic
#     overload_rules:
#       - status: 400
#         body_contains: "overloaded"
#         max_retries: 10
#         delay: 2s
#         jitter: 1s
```

- [ ] **Step 2: Commit**

```bash
git add config.yaml
git commit -m "feat(config): add multi-protocol routing config example"
```

---

### Task 6: 更新 README 文档

**Files:**
- Modify: `README.md`

- [ ] **Step 1: 添加多协议路由说明**

在 README.md 的"内置 Provider"章节之前添加新章节：

```markdown
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
    url: https://modelservice.jdcloud.com/coding/openai/v1
    protocol: openai

routes:
  - path: /v1/messages
    upstream: anthropic
  - path: /v1/chat/completions
    upstream: openai
```

客户端只需配置代理地址 `http://127.0.0.1:8087`，协议识别完全自动。

---
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: add multi-protocol routing documentation"
```

---

### Task 7: 验证构建

**Files:**
- 无文件变更，仅验证

- [ ] **Step 1: 运行 go vet 检查**

```bash
cd d:/Dev/anthropic-proxy && go vet ./...
```

Expected: 无错误输出

- [ ] **Step 2: 运行构建**

```bash
cd d:/Dev/anthropic-proxy && go build -o bin/anthropic-proxy ./cmd/anthropic-proxy
```

Expected: 构建成功，无错误

- [ ] **Step 3: 最终提交（如有遗漏）**

```bash
git status
```

如有未提交文件，补充提交。

---

## 成功标准

1. 新配置格式正确加载，路由按路径匹配
2. 旧配置格式继续正常工作（向后兼容）
3. 未匹配路径返回 404 JSON 错误
4. Token 统计使用对应协议的 Parser
5. 构建无错误，go vet 通过
