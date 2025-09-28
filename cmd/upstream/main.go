package main

import (
	"log"
	"os"
	"strings"
	"sync"

	"traefik-challenge-2/internal/upstream"

	"gopkg.in/yaml.v3"
)

type StringList []string

func (s *StringList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var v string
		if err := value.Decode(&v); err != nil {
			return err
		}
		v = strings.TrimSpace(v)
		if v == "" {
			*s = nil
			return nil
		}
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		*s = out
		return nil
	case yaml.SequenceNode:
		out := make([]string, 0, len(value.Content))
		for _, n := range value.Content {
			var it string
			if err := n.Decode(&it); err != nil {
				return err
			}
			if it = strings.TrimSpace(it); it != "" {
				out = append(out, it)
			}
		}
		*s = out
		return nil
	default:
		*s = nil
		return nil
	}
}

type upstreamYAML struct {
	Upstream *struct {
		Listen StringList `yaml:"listen"`
	} `yaml:"upstream"`
}

func loadUpstreamListens() []string {
	// Default if nothing else provided
	addrs := []string{":8000"}

	// Prefer CONFIG_FILE if present
	cfgFile := strings.TrimSpace(os.Getenv("CONFIG_FILE"))
	if cfgFile == "" {
		// Try common defaults in CWD and configs/
		for _, c := range []string{"configs/config.yaml", "configs/config.yml"} {
			if _, err := os.Stat(c); err == nil {
				cfgFile = c
				break
			}
		}
	}
	if cfgFile != "" {
		if b, err := os.ReadFile(cfgFile); err == nil {
			var y upstreamYAML
			if err := yaml.Unmarshal(b, &y); err == nil {
				if y.Upstream != nil && len(y.Upstream.Listen) > 0 {
					return y.Upstream.Listen
				}
			}
		}
	}

	// Legacy fallback env if YAML absent (kept for convenience)
	if v := strings.TrimSpace(os.Getenv("UPSTREAM_LISTEN")); v != "" {
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return addrs
}

func main() {
	addrs := loadUpstreamListens()

	// Start one server per address
	if len(addrs) > 1 {
		var wg sync.WaitGroup
		for _, a := range addrs {
			a = strings.TrimSpace(a)
			if a == "" {
				continue
			}
			wg.Add(1)
			go func(ad string) {
				defer wg.Done()
				log.Printf("starting upstream server on %s", ad)
				if err := upstream.Start(ad); err != nil {
					log.Printf("upstream server %s exited: %v", ad, err)
				}
			}(a)
		}
		wg.Wait()
		return
	}

	addr := strings.TrimSpace(addrs[0])
	log.Printf("starting upstream server on %s", addr)
	if err := upstream.Start(addr); err != nil {
		log.Fatal(err)
	}
}

