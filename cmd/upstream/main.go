/*
Example upstream HTTP server used for local development and demos.

Typical usage:
- Start the server and access it via: http://localhost:8000
- Configuration is read only from YAML (configs/config.yaml or configs/config.yml).

Example YAML:
upstream:

		# Single value (string)
		listen: ":8000"

		# Or multiple values (list)
		upstream:
	 	listen: [":9000", ":9001", ":9002"]

Note: This is a simple example app, not a production-ready server.
*/
package main

import (
	"log"
	"os"
	"strings"
	"sync"
	"traefik-challenge-2/internal/upstream"

	"gopkg.in/yaml.v3"
)

// StringList allows YAML "listen" to be either a comma-separated string or a YAML sequence.
// It trims whitespace and ignores empty items so sample/demo configs are forgiving.
type StringList []string

func main() {
	// Resolve listen addresses strictly from YAML.
	listenAddrs := loadListenAddressesFromYAML()

	// Start one server per address (useful if you demo multiple ports).
	if len(listenAddrs) > 1 {
		var serversWG sync.WaitGroup
		for _, addr := range listenAddrs {
			addr = strings.TrimSpace(addr)
			if addr == "" {
				continue
			}
			serversWG.Add(1)
			go func(addr string) {
				defer serversWG.Done()
				log.Printf("starting upstream server on %s", addr)
				if err := upstream.Start(addr); err != nil {
					log.Printf("upstream server %s exited: %v", addr, err)
				}
			}(addr)
		}
		serversWG.Wait()
		return
	}

	// Single-address case: start the example server on the first address
	addr := strings.TrimSpace(listenAddrs[0])
	log.Printf("starting upstream server on %s", addr)
	if err := upstream.Start(addr); err != nil {
		log.Fatal(err)
	}
}


// upstreamYAML mirrors only the part of the config we need for this example server.
type upstreamYAML struct {
	Upstream *struct {
		Listen StringList `yaml:"listen"`
	} `yaml:"upstream"`
}

// loadListenAddressesFromYAML returns the upstream listen addresses using only YAML configuration.
// Falls back to [":8000"] if no config is found or the YAML has no listen values.
func loadListenAddressesFromYAML() []string {
	// Default address when no YAML is present or listen list is empty.
	defaultAddresses := []string{":8000"}

	// Candidates in configs/ folder beside the binary during local demos.
	candidates := []string{
		"configs/config-upstream.yaml", "configs/config-upstream.yml",
	}

	// Pick the first candidate that exists.
	var configPath string
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			configPath = c
			break
		}
	}

	// If we found a config file, parse it and use upstream.listen if present.
	if configPath != "" {
		if b, err := os.ReadFile(configPath); err == nil {
			var cfg upstreamYAML
			if err := yaml.Unmarshal(b, &cfg); err == nil {
				if cfg.Upstream != nil && len(cfg.Upstream.Listen) > 0 {
					return cfg.Upstream.Listen
				}
			}
		}
	}

	// No YAML found or no valid listen entries; return default.
	return defaultAddresses
}



