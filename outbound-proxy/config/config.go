package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the root YAML schema loaded by the proxy.
type Config struct {
	User         string        `yaml:"user"`
	ManagedTools []ManagedTool `yaml:"managed_tools"`
	Blocked      []string      `yaml:"blocked"`
}

// ManagedTool describes one managed destination and credential/policy behavior.
type ManagedTool struct {
	Name        string      `yaml:"name"`
	Match       string      `yaml:"match"`
	Protocol    string      `yaml:"protocol"`
	LocalPort   int         `yaml:"local_port"`
	Description string      `yaml:"description"`
	Database    string      `yaml:"database"`
	Credentials Credentials `yaml:"credentials"`
	Policy      Policy      `yaml:"policy"`
}

// Credentials holds source metadata and material needed for injection.
type Credentials struct {
	Source      string `yaml:"source"`
	Ref         string `yaml:"ref"`
	HeaderName  string `yaml:"header_name"`
	HeaderValue string `yaml:"header_value"`
	Username    string `yaml:"username"`
	Password    string `yaml:"password"`
}

// Policy constrains access to a managed tool.
type Policy struct {
	Methods   []string `yaml:"methods"`
	Paths     []string `yaml:"paths"`
	RateLimit string   `yaml:"rate_limit"`
}

// Load reads a YAML config file and applies environment variable expansion.
func Load(path string) (*Config, error) {
	if path == "" {
		return nil, fmt.Errorf("config path is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	for i := range cfg.ManagedTools {
		if cfg.ManagedTools[i].Protocol == "" {
			cfg.ManagedTools[i].Protocol = "http"
		}
		cfg.ManagedTools[i].Credentials.Source = os.ExpandEnv(cfg.ManagedTools[i].Credentials.Source)
		cfg.ManagedTools[i].Credentials.Ref = os.ExpandEnv(cfg.ManagedTools[i].Credentials.Ref)
		cfg.ManagedTools[i].Credentials.HeaderName = os.ExpandEnv(cfg.ManagedTools[i].Credentials.HeaderName)
		cfg.ManagedTools[i].Credentials.HeaderValue = os.ExpandEnv(cfg.ManagedTools[i].Credentials.HeaderValue)
		cfg.ManagedTools[i].Credentials.Username = os.ExpandEnv(cfg.ManagedTools[i].Credentials.Username)
		cfg.ManagedTools[i].Credentials.Password = os.ExpandEnv(cfg.ManagedTools[i].Credentials.Password)
	}

	return &cfg, nil
}
