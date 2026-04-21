// Package config handles loading and resolving proxy configuration from a YAML file.
//
// Environment variable overrides (applied after file is parsed):
//   - PROVIDER     — overrides the 'active' field
//   - UPSTREAM_URL — overrides the active provider's upstream URL
//   - LISTEN_ADDR  — overrides the top-level listen address
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"anthropic-proxy/internal/provider"

	"gopkg.in/yaml.v3"
)

// Default retry values applied when a rule omits the field.
const (
	defaultListenAddr  = ":8080"
	defaultMaxRetries  = 10
	defaultRetryDelay  = 2 * time.Second
	defaultRetryJitter = 1 * time.Second
)

// Config is the resolved runtime configuration for the active provider.
type Config struct {
	ListenAddr    string
	Upstream      string
	ProviderName  string
	OverloadRules []provider.Rule
	// Protocol identifies the API response format for token usage parsing.
	// Supported values: "anthropic" (default), "openai".
	Protocol string
	// StatsDB is the path to the SQLite database used for token usage statistics.
	// Empty means stats are disabled.
	StatsDB string
}

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

// ---- YAML types ----

type yamlDuration struct{ time.Duration }

func (d *yamlDuration) UnmarshalYAML(value *yaml.Node) error {
	dur, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value.Value, err)
	}
	d.Duration = dur
	return nil
}

type ruleYAML struct {
	Status       int           `yaml:"status"`
	BodyContains string        `yaml:"body_contains"`
	MaxRetries   *int          `yaml:"max_retries"`
	Delay        *yamlDuration `yaml:"delay"`
	Jitter       *yamlDuration `yaml:"jitter"`
}

type providerYAML struct {
	Upstream      string     `yaml:"upstream"`
	Protocol      string     `yaml:"protocol"`
	OverloadRules []ruleYAML `yaml:"overload_rules"`
}

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

type fileConfig struct {
	Listen    string                  `yaml:"listen"`
	Active    string                  `yaml:"active"`
	StatsDB   string                  `yaml:"stats_db"`
	Providers map[string]providerYAML `yaml:"providers"`
}

// Load reads the YAML config file at path and returns the resolved Config.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var fc fileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	if v := os.Getenv("PROVIDER"); v != "" {
		fc.Active = v
	}
	if fc.Active == "" {
		return nil, fmt.Errorf("config: set 'active' in config.yaml or via PROVIDER env var")
	}

	pc, ok := fc.Providers[fc.Active]
	if !ok {
		return nil, fmt.Errorf("config: provider %q not found in config.yaml", fc.Active)
	}

	if v := os.Getenv("UPSTREAM_URL"); v != "" {
		pc.Upstream = v
	}
	if v := os.Getenv("STATS_DB"); v != "" {
		fc.StatsDB = v
	}

	return resolve(fc.Active, pc, fc.Listen, fc.StatsDB)
}

func resolve(name string, pc providerYAML, listen, statsDB string) (*Config, error) {
	if pc.Upstream == "" {
		return nil, fmt.Errorf("provider %q: upstream URL is required", name)
	}
	if len(pc.OverloadRules) == 0 {
		return nil, fmt.Errorf("provider %q: overload_rules must not be empty", name)
	}

	if listen == "" {
		listen = defaultListenAddr
	}

	rules := make([]provider.Rule, len(pc.OverloadRules))
	for i, r := range pc.OverloadRules {
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

	protocol := pc.Protocol
	if protocol == "" {
		protocol = "anthropic"
	}

	return &Config{
		ListenAddr:    listen,
		Upstream:      strings.TrimRight(pc.Upstream, "/"),
		ProviderName:  name,
		OverloadRules: rules,
		Protocol:      protocol,
		StatsDB:       statsDB,
	}, nil
}

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
