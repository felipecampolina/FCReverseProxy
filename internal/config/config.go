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

// TLSConfig holds TLS enablement and file paths for certificate and key.
type TLSConfig struct {
	Enabled  bool
	CertFile string
	KeyFile  string
}

// Config holds all runtime settings derived from YAML and defaults.
type Config struct {
	ListenAddr              string     // Example: ":8080"
	TargetURL               *url.URL   // First (primary) target for backward compatibility
	TargetURLs              []*url.URL // All targets (>=1)
	Cache                   CacheConfig
	Queue                   proxy.QueueConfig
	AllowedMethods          []string
	LoadBalancerStrategy    string
	LoadBalancerHealthCheck bool
	TLS                     TLSConfig
}

// CacheConfig configures the in-memory response cache.
type CacheConfig struct {
	Enabled    bool
	MaxEntries int
	TTL        time.Duration
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
	defaultLBHealthCheck       = true
	defaultLBStrategy          = "rr"
	defaultCacheTTL            = 60 * time.Second
)

// --- YAML model (pointers used so we can distinguish "omitted" vs "false/zero") ---
// yamlRoot represents the top-level YAML document.
type yamlRoot struct {
	Proxy    *yamlProxy    `yaml:"proxy"`
	Upstream *yamlUpstream `yaml:"upstream"`
}

// yamlProxy mirrors the "proxy" section of the YAML configuration.
type yamlProxy struct {
	Listen                  *string    `yaml:"listen"`
	Targets                 []string   `yaml:"targets"`
	LoadBalancerStrategy    *string    `yaml:"load_balancer_strategy"`
	LoadBalancerHealthCheck *bool      `yaml:"load_balancer_health_check"`
	AllowedMethods          []string   `yaml:"allowed_methods"`
	Cache                   *yamlCache `yaml:"cache"`
	Queue                   *yamlQueue `yaml:"queue"`
	TLS                     *yamlTLS   `yaml:"tls"`
}

// yamlCache mirrors the "proxy.cache" section.
type yamlCache struct {
	Enabled    *bool   `yaml:"enabled"`
	MaxEntries *int    `yaml:"max_entries"`
	TTL        *string `yaml:"ttl"`
}

// yamlQueue mirrors the "proxy.queue" section.
type yamlQueue struct {
	MaxQueue        *int    `yaml:"max_queue"`
	MaxConcurrent   *int    `yaml:"max_concurrent"`
	EnqueueTimeout  *string `yaml:"enqueue_timeout"`
	QueueWaitHeader *bool   `yaml:"queue_wait_header"`
}

// yamlTLS mirrors the "proxy.tls" section.
type yamlTLS struct {
	Enabled  *bool   `yaml:"enabled"`
	CertFile *string `yaml:"cert_file"`
	KeyFile  *string `yaml:"key_file"`
}

// yamlUpstream exists for backward-compatibility (unused for now).
type yamlUpstream struct {
	Listen any `yaml:"listen"` // accept string or list
}

// Load reads configuration from YAML, applies defaults, normalizes values,
// and returns a ready-to-use Config instance.
func Load() (*Config, error) {
	// Find the config file path.
	configFilePath, err := findConfigFile()
	if err != nil {
		return nil, err
	}

	// Read the YAML configuration file from disk.
	fileBytes, err := os.ReadFile(configFilePath)
	if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", configFilePath, err)
	}

	// Unmarshal into the YAML model so we can tell "omitted" vs "explicit zero/false".
	var yamlRootCfg yamlRoot
	if err := yaml.Unmarshal(fileBytes, &yamlRootCfg); err != nil {
		return nil, fmt.Errorf("parse yaml %s: %w", configFilePath, err)
	}
	if yamlRootCfg.Proxy == nil {
		return nil, errors.New("config: proxy section is required")
	}

	// Initialize with sane defaults.
	cfg := &Config{
		ListenAddr: defaultListen,
		Cache: CacheConfig{
			Enabled:    defaultCacheEnabled,
			MaxEntries: defaultCacheMaxEntries,
			TTL:        defaultCacheTTL,
		},
		Queue: proxy.QueueConfig{
			MaxQueue:        defaultQueueMax,
			MaxConcurrent:   defaultQueueMaxConcurrent,
			EnqueueTimeout:  defaultQueueEnqueueTimeout,
			QueueWaitHeader: defaultQueueWaitHeader,
		},
		AllowedMethods:          parseMethods(defaultAllowedMethods),
		LoadBalancerStrategy:    defaultLBStrategy,
		LoadBalancerHealthCheck: defaultLBHealthCheck,
		TLS: TLSConfig{
			Enabled:  false,
			CertFile: "",
			KeyFile:  "",
		},
	}

	// Apply proxy.listen if provided.
	if listenValue := yamlRootCfg.Proxy.Listen; listenValue != nil && strings.TrimSpace(*listenValue) != "" {
		cfg.ListenAddr = strings.TrimSpace(*listenValue)
	}

	// Collect and validate at least one target (proxy.targets only).
	var rawTargetStrings []string
	if len(yamlRootCfg.Proxy.Targets) > 0 {
		rawTargetStrings = yamlRootCfg.Proxy.Targets
	}
	if len(rawTargetStrings) == 0 {
		return nil, errors.New(`config: proxy.targets must be defined with at least one URL (e.g., ["http://localhost:9000"])`)
	}

	// Parse and validate each target URL.
	var parsedTargetURLs []*url.URL
	for _, targetStr := range rawTargetStrings {
		parsedURL, err := url.Parse(strings.TrimSpace(targetStr))
		if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
			return nil, fmt.Errorf("config: invalid target %q", targetStr)
		}
		parsedTargetURLs = append(parsedTargetURLs, parsedURL)
	}
	cfg.TargetURLs = parsedTargetURLs
	cfg.TargetURL = parsedTargetURLs[0] // first item remains the primary target

	// Load balancer strategy (optional).
	if yamlRootCfg.Proxy.LoadBalancerStrategy != nil && strings.TrimSpace(*yamlRootCfg.Proxy.LoadBalancerStrategy) != "" {
		cfg.LoadBalancerStrategy = strings.TrimSpace(*yamlRootCfg.Proxy.LoadBalancerStrategy)
	}
	// Load balancer health check (optional).
	if yamlRootCfg.Proxy.LoadBalancerHealthCheck != nil {
		cfg.LoadBalancerHealthCheck = *yamlRootCfg.Proxy.LoadBalancerHealthCheck
	}

	// Allowed HTTP methods (optional). Normalize to upper-case unique values.
	if len(yamlRootCfg.Proxy.AllowedMethods) > 0 {
		cfg.AllowedMethods = parseMethods(strings.Join(yamlRootCfg.Proxy.AllowedMethods, ","))
	}

	// Cache section (optional).
	if yamlRootCfg.Proxy.Cache != nil {
		if yamlRootCfg.Proxy.Cache.Enabled != nil {
			cfg.Cache.Enabled = *yamlRootCfg.Proxy.Cache.Enabled
		}
		if yamlRootCfg.Proxy.Cache.MaxEntries != nil && *yamlRootCfg.Proxy.Cache.MaxEntries > 0 {
			cfg.Cache.MaxEntries = *yamlRootCfg.Proxy.Cache.MaxEntries
		}
		if yamlRootCfg.Proxy.Cache.TTL != nil && strings.TrimSpace(*yamlRootCfg.Proxy.Cache.TTL) != "" {
			if parsed, err := time.ParseDuration(strings.TrimSpace(*yamlRootCfg.Proxy.Cache.TTL)); err == nil && parsed > 0 {
				cfg.Cache.TTL = parsed
			} else if err != nil {
				return nil, fmt.Errorf("config: invalid cache.ttl: %v", err)
			}
		}
	}

	// Queue section (optional).
	if yamlRootCfg.Proxy.Queue != nil {
		if yamlRootCfg.Proxy.Queue.MaxQueue != nil && *yamlRootCfg.Proxy.Queue.MaxQueue > 0 {
			cfg.Queue.MaxQueue = *yamlRootCfg.Proxy.Queue.MaxQueue
		}
		if yamlRootCfg.Proxy.Queue.MaxConcurrent != nil && *yamlRootCfg.Proxy.Queue.MaxConcurrent > 0 {
			cfg.Queue.MaxConcurrent = *yamlRootCfg.Proxy.Queue.MaxConcurrent
		}
		if yamlRootCfg.Proxy.Queue.EnqueueTimeout != nil && strings.TrimSpace(*yamlRootCfg.Proxy.Queue.EnqueueTimeout) != "" {
			// Parse Go duration strings like "2s", "500ms", etc.
			if parsedDuration, err := time.ParseDuration(strings.TrimSpace(*yamlRootCfg.Proxy.Queue.EnqueueTimeout)); err == nil {
				cfg.Queue.EnqueueTimeout = parsedDuration
			} else {
				return nil, fmt.Errorf("config: invalid queue.enqueue_timeout: %v", err)
			}
		}
		if yamlRootCfg.Proxy.Queue.QueueWaitHeader != nil {
			cfg.Queue.QueueWaitHeader = *yamlRootCfg.Proxy.Queue.QueueWaitHeader
		}
	}

	// TLS section (optional).
	if yamlRootCfg.Proxy.TLS != nil {
		if yamlRootCfg.Proxy.TLS.Enabled != nil {
			cfg.TLS.Enabled = *yamlRootCfg.Proxy.TLS.Enabled
		}
		if yamlRootCfg.Proxy.TLS.CertFile != nil {
			cfg.TLS.CertFile = strings.TrimSpace(*yamlRootCfg.Proxy.TLS.CertFile)
		}
		if yamlRootCfg.Proxy.TLS.KeyFile != nil {
			cfg.TLS.KeyFile = strings.TrimSpace(*yamlRootCfg.Proxy.TLS.KeyFile)
		}
	}

	// Apply default cache TTL to proxy package.
	proxy.SetDefaultCacheTTL(cfg.Cache.TTL)

	return cfg, nil
}

// findConfigFile locates a config file path by:
func findConfigFile() (string, error) {
	defaultPath := "configs/config.yaml"
	if _, statErr := os.Stat(defaultPath); statErr == nil {
		return defaultPath, nil
	}
	return "", errors.New("config file not found (create configs/config.yaml)")
}

// parseMethods converts a comma-separated string of HTTP methods into a slice
// of unique, upper-case method names, preserving only non-empty entries.
func parseMethods(methodsCSV string) []string {
	if methodsCSV == "" {
		return nil
	}

	rawMethods := strings.Split(methodsCSV, ",")
	normalizedMethods := make([]string, 0, len(rawMethods))
	uniqueMethods := make(map[string]struct{}, len(rawMethods))

	for _, rawMethod := range rawMethods {
		method := strings.ToUpper(strings.TrimSpace(rawMethod))
		if method == "" {
			continue
		}
		if _, exists := uniqueMethods[method]; exists {
			continue
		}
		uniqueMethods[method] = struct{}{}
		normalizedMethods = append(normalizedMethods, method)
	}

	return normalizedMethods
}
