// internal/sim/config.go
package sim

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the cabinet-sim binary's YAML configuration, loaded via the
// -config flag. Env override (VIKASA_*) is not needed in v1.
type Config struct {
	Dot      string  `yaml:"dot"`
	District string  `yaml:"district"`
	Cabinet  string  `yaml:"cabinet"`
	Vendor   string  `yaml:"vendor"`
	Seed     int64   `yaml:"seed"`
	BaseVPH  float64 `yaml:"base_vph"`
	NATSURL  string  `yaml:"nats_url"`  // local leaf, e.g. nats://gdot-d1-cab-i85-001-nats:4222
	HTTPAddr string  `yaml:"http_addr"` // :8080 for /inject and /healthz
	// Reversible marks the one I-75 South cabinet that also controls a
	// reversible express-lane segment; it adds a ReversibleLane device on top
	// of the standard 6-device roster. False for every other cabinet.
	Reversible bool `yaml:"reversible"`
}

// LoadConfig reads and parses a Config from a YAML file at path.
func LoadConfig(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}
