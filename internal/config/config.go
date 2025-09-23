package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"traefik-challenge-2/internal/proxy"
)

type Config struct {
	ListenAddr string   // Example: ":8080"
	TargetURL  *url.URL // Example: "http://localhost:9000"
	Cache      CacheConfig
	Queue      proxy.QueueConfig
}

type CacheConfig struct {
	Enabled    bool
	MaxEntries int
}

type QueueConfig struct {
	MaxQueue        int
	MaxConcurrent   int
	EnqueueTimeout  time.Duration
	QueueWaitHeader bool
}

const (
	defaultListen              = ":8080"
	defaultCacheEnabled        = true
	defaultCacheMaxEntries     = 2048
	defaultQueueMax            = 1000
	defaultQueueMaxConcurrent  = 100
	defaultQueueEnqueueTimeout = 2 * time.Second
	defaultQueueWaitHeader     = true
)

// Load reads environment variables and returns a validated Config.
func Load() (*Config, error) {
	listen := getEnv("PROXY_LISTEN", defaultListen)

	rawTarget := strings.TrimSpace(os.Getenv("PROXY_TARGET"))
	if rawTarget == "" {
		return nil, errors.New("PROXY_TARGET is not defined (e.g., http://localhost:9000)")
	}

	u, err := url.Parse(rawTarget)
	if err != nil {
		return nil, fmt.Errorf("invalid PROXY_TARGET: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, errors.New("PROXY_TARGET must include scheme and host (e.g., http://localhost:9000)")
	}

	cacheEnabled := getEnvBool("CACHE_ENABLED", defaultCacheEnabled)
	cacheMax := getEnvInt("CACHE_MAX_ENTRIES", defaultCacheMaxEntries)

	// Queue configuration (moved here)
	q := proxy.QueueConfig{
		MaxQueue:        getEnvInt("RP_MAX_QUEUE", defaultQueueMax),
		MaxConcurrent:   getEnvInt("RP_MAX_CONCURRENT", defaultQueueMaxConcurrent),
		EnqueueTimeout:  getEnvDuration("RP_ENQUEUE_TIMEOUT", defaultQueueEnqueueTimeout),
		QueueWaitHeader: getEnvBool("RP_QUEUE_WAIT_HEADER", defaultQueueWaitHeader),
	}

	return &Config{
		ListenAddr: listen,
		TargetURL:  u,
		Cache: CacheConfig{
			Enabled:    cacheEnabled,
			MaxEntries: cacheMax,
		},
		Queue: q,
	}, nil
}

// Retrieves an environment variable or returns the default value.
func getEnv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// Retrieves a boolean environment variable or returns the default value.
func getEnvBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return parsed
}

// Retrieves an integer environment variable or returns the default value.
func getEnvInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	parsed, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return parsed
}

func getEnvDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
