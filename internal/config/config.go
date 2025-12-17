package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config describes the top-level configuration for the migrator.
type Config struct {
	Storage StorageConfig `yaml:"storage"`
	Pairs   []PairConfig  `yaml:"pairs"`

	// SourcePath remembers where the config was loaded from to resolve relative paths.
	SourcePath string `yaml:"-"`
}

// StorageConfig describes where migration scripts are stored.
type StorageConfig struct {
	Path string `yaml:"path"`
}

// PairConfig binds staging and production databases together.
type PairConfig struct {
	Name           string   `yaml:"name"`
	MigrationTable string   `yaml:"migration_table"`
	Staging        DBConfig `yaml:"staging"`
	Production     DBConfig `yaml:"production"`
}

// DBConfig holds details needed to connect to a database.
type DBConfig struct {
	Provider string `yaml:"provider"` // postgres or mysql
	DSN      string `yaml:"dsn"`
	Schema   string `yaml:"schema"` // optional, defaults to public for postgres or current database for mysql
}

// Load reads a YAML configuration file from disk.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.SourcePath, _ = filepath.Abs(path)
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Storage.Path == "" {
		c.Storage.Path = "storage"
	}

	for i := range c.Pairs {
		if c.Pairs[i].MigrationTable == "" {
			c.Pairs[i].MigrationTable = "migration_status"
		}
		if c.Pairs[i].Staging.Schema == "" && strings.ToLower(c.Pairs[i].Staging.Provider) == "postgres" {
			c.Pairs[i].Staging.Schema = "public"
		}
		if c.Pairs[i].Production.Schema == "" && strings.ToLower(c.Pairs[i].Production.Provider) == "postgres" {
			c.Pairs[i].Production.Schema = "public"
		}
	}
}

// Validate ensures the configuration has all required fields.
func (c *Config) Validate() error {
	if len(c.Pairs) == 0 {
		return fmt.Errorf("no database pairs defined")
	}

	for _, p := range c.Pairs {
		if p.Name == "" {
			return fmt.Errorf("pair is missing name")
		}
		if err := validateDB(p.Staging); err != nil {
			return fmt.Errorf("pair %s staging: %w", p.Name, err)
		}
		if err := validateDB(p.Production); err != nil {
			return fmt.Errorf("pair %s production: %w", p.Name, err)
		}
	}
	return nil
}

func validateDB(db DBConfig) error {
	provider := strings.ToLower(db.Provider)
	if provider != "postgres" && provider != "mysql" {
		return fmt.Errorf("provider must be postgres or mysql, got %s", db.Provider)
	}
	if db.DSN == "" {
		return fmt.Errorf("dsn must be provided")
	}
	return nil
}

// Pair returns the pair configuration for a given name.
func (c *Config) Pair(name string) (*PairConfig, error) {
	for i := range c.Pairs {
		if c.Pairs[i].Name == name {
			return &c.Pairs[i], nil
		}
	}
	return nil, fmt.Errorf("pair %s not found", name)
}

// StoragePath resolves the storage directory, allowing relative paths next to the config file.
func (c *Config) StoragePath() string {
	if filepath.IsAbs(c.Storage.Path) {
		return c.Storage.Path
	}
	base := filepath.Dir(c.SourcePath)
	return filepath.Join(base, c.Storage.Path)
}
