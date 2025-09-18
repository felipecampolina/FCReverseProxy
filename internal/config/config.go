package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	ListenAddr string   // Ex: ":8080"
	TargetURL  *url.URL // Ex: "http://localhost:9000"
}

const (
	defaultListen = ":8080"
)

// Load lê variáveis de ambiente e retorna uma Config validada
func Load() (*Config, error) {
	listen := getEnv("PROXY_LISTEN", defaultListen)

	rawTarget := strings.TrimSpace(os.Getenv("PROXY_TARGET"))
	if rawTarget == "" {
		return nil, errors.New("PROXY_TARGET não definido (ex: http://localhost:9000)")
	}

	u, err := url.Parse(rawTarget)
	if err != nil {
		return nil, fmt.Errorf("PROXY_TARGET inválido: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, errors.New("PROXY_TARGET precisa ter esquema e host (ex: http://localhost:9000)")
	}

	return &Config{
		ListenAddr: listen,
		TargetURL:  u,
	}, nil
}

func getEnv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
