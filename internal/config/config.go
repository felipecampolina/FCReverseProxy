package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
	"traefik-challenge-2/internal/proxy"

	"gopkg.in/yaml.v3"
)

type TLSConfig struct {
	Enabled  bool
	CertFile string
	KeyFile  string
}

type Config struct {
	ListenAddr           string     // Example: ":8080"
	TargetURL            *url.URL   // First (primary) target for backward compatibility
	TargetURLs           []*url.URL // All targets (>=1)
	Cache                CacheConfig
	Queue                proxy.QueueConfig
	AllowedMethods       []string
	LoadBalancerStrategy string
	TLS                  TLSConfig
}

type CacheConfig struct {
	Enabled    bool
	MaxEntries int
}

const (
	defaultListen              = ":8080"
	defaultCacheEnabled        = true
	defaultCacheMaxEntries     = 2048
	defaultQueueMax            = 1000
	defaultQueueMaxConcurrent  = 100
	defaultQueueEnqueueTimeout = 2 * time.Second
	defaultQueueWaitHeader     = true
	defaultAllowedMethods      = "GET,HEAD,POST,PUT,PATCH,DELETE"
)

// --- YAML model (pointers used so we can distinguish "omitted" vs "false/zero") ---

type yamlRoot struct {
	Proxy    *yamlProxy    `yaml:"proxy"`
	Upstream *yamlUpstream `yaml:"upstream"`
}
type yamlProxy struct {
	Listen               *string     `yaml:"listen"`
	Targets              []string    `yaml:"targets"`
	Target               *string     `yaml:"target"`
	LoadBalancerStrategy *string     `yaml:"load_balancer_strategy"`
	AllowedMethods       []string    `yaml:"allowed_methods"`
	Cache                *yamlCache  `yaml:"cache"`
	Queue                *yamlQueue  `yaml:"queue"`
	TLS                  *yamlTLS    `yaml:"tls"`
}
type yamlCache struct {
	Enabled    *bool `yaml:"enabled"`
	MaxEntries *int  `yaml:"max_entries"`
}
type yamlQueue struct {
	MaxQueue        *int    `yaml:"max_queue"`
	MaxConcurrent   *int    `yaml:"max_concurrent"`
	EnqueueTimeout  *string `yaml:"enqueue_timeout"`
	QueueWaitHeader *bool   `yaml:"queue_wait_header"`
}
type yamlTLS struct {
	Enabled  *bool   `yaml:"enabled"`
	CertFile *string `yaml:"cert_file"`
	KeyFile  *string `yaml:"key_file"`
}
type yamlUpstream struct {
	Listen any `yaml:"listen"` // accept string or list
}

// Load reads configuration from YAML 
func Load() (*Config, error) {
	path, err := findConfigFile()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}

	var y yamlRoot
	if err := yaml.Unmarshal(data, &y); err != nil {
		return nil, fmt.Errorf("parse yaml %s: %w", path, err)
	}
	if y.Proxy == nil {
		return nil, errors.New("config: proxy section is required")
	}

	// Defaults
	cfg := &Config{
		ListenAddr: defaultListen,
		Cache: CacheConfig{
			Enabled:    defaultCacheEnabled,
			MaxEntries: defaultCacheMaxEntries,
		},
		Queue: proxy.QueueConfig{
			MaxQueue:        defaultQueueMax,
			MaxConcurrent:   defaultQueueMaxConcurrent,
			EnqueueTimeout:  defaultQueueEnqueueTimeout,
			QueueWaitHeader: defaultQueueWaitHeader,
		},
		AllowedMethods:       parseMethods(defaultAllowedMethods),
		LoadBalancerStrategy: "rr",
		TLS: TLSConfig{
			Enabled:  false,
			CertFile: "",
			KeyFile:  "",
		},
	}

	// Apply proxy.listen
	if v := y.Proxy.Listen; v != nil && strings.TrimSpace(*v) != "" {
		cfg.ListenAddr = strings.TrimSpace(*v)
	}

	// Apply targets/target (need at least 1)
	var rawTargets []string
	if len(y.Proxy.Targets) > 0 {
		rawTargets = y.Proxy.Targets
	} else if y.Proxy.Target != nil && strings.TrimSpace(*y.Proxy.Target) != "" {
		rawTargets = []string{strings.TrimSpace(*y.Proxy.Target)}
	}
	if len(rawTargets) == 0 {
		return nil, errors.New("config: proxy.targets or proxy.target must be defined (e.g., http://localhost:9000)")
	}
	var parsed []*url.URL
	for _, s := range rawTargets {
		u, err := url.Parse(strings.TrimSpace(s))
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("config: invalid target %q", s)
		}
		parsed = append(parsed, u)
	}
	cfg.TargetURLs = parsed
	cfg.TargetURL = parsed[0]

	// Load balancer strategy
	if y.Proxy.LoadBalancerStrategy != nil && strings.TrimSpace(*y.Proxy.LoadBalancerStrategy) != "" {
		cfg.LoadBalancerStrategy = strings.TrimSpace(*y.Proxy.LoadBalancerStrategy)
	}

	// Allowed methods (optional)
	if len(y.Proxy.AllowedMethods) > 0 {
		cfg.AllowedMethods = parseMethods(strings.Join(y.Proxy.AllowedMethods, ","))
	}

	// Cache
	if y.Proxy.Cache != nil {
		if y.Proxy.Cache.Enabled != nil {
			cfg.Cache.Enabled = *y.Proxy.Cache.Enabled
		}
		if y.Proxy.Cache.MaxEntries != nil && *y.Proxy.Cache.MaxEntries > 0 {
			cfg.Cache.MaxEntries = *y.Proxy.Cache.MaxEntries
		}
	}

	// Queue
	if y.Proxy.Queue != nil {
		if y.Proxy.Queue.MaxQueue != nil && *y.Proxy.Queue.MaxQueue > 0 {
			cfg.Queue.MaxQueue = *y.Proxy.Queue.MaxQueue
		}
		if y.Proxy.Queue.MaxConcurrent != nil && *y.Proxy.Queue.MaxConcurrent > 0 {
			cfg.Queue.MaxConcurrent = *y.Proxy.Queue.MaxConcurrent
		}
		if y.Proxy.Queue.EnqueueTimeout != nil && strings.TrimSpace(*y.Proxy.Queue.EnqueueTimeout) != "" {
			if d, err := time.ParseDuration(strings.TrimSpace(*y.Proxy.Queue.EnqueueTimeout)); err == nil {
				cfg.Queue.EnqueueTimeout = d
			} else {
				return nil, fmt.Errorf("config: invalid queue.enqueue_timeout: %v", err)
			}
		}
		if y.Proxy.Queue.QueueWaitHeader != nil {
			cfg.Queue.QueueWaitHeader = *y.Proxy.Queue.QueueWaitHeader
		}
	}

	// TLS
	if y.Proxy.TLS != nil {
		if y.Proxy.TLS.Enabled != nil {
			cfg.TLS.Enabled = *y.Proxy.TLS.Enabled
		}
		if y.Proxy.TLS.CertFile != nil {
			cfg.TLS.CertFile = strings.TrimSpace(*y.Proxy.TLS.CertFile)
		}
		if y.Proxy.TLS.KeyFile != nil {
			cfg.TLS.KeyFile = strings.TrimSpace(*y.Proxy.TLS.KeyFile)
		}
	}

	return cfg, nil
}
func findConfigFile() (string, error) {
	// Allow overriding via CONFIG_FILE for tests or custom deployments.
	if v := strings.TrimSpace(os.Getenv("CONFIG_FILE")); v != "" {
		return v, nil
	}
	// Try configs/config.yaml then configs/config.yml in the root directory.
	candidates := []string{"configs/config.yaml", "configs/config.yml"}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", errors.New("config file not found (create configs/config.yaml or set CONFIG_FILE)")
}


// parseMethods converts comma-separated methods to upper-case slice.
func parseMethods(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		m := strings.ToUpper(strings.TrimSpace(p))
		if m == "" {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	return out
}
