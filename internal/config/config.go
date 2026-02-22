package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listener ListenerConfig `yaml:"listener"`
	Backend  BackendConfig  `yaml:"backend"`
	Pool     PoolConfig     `yaml:"pool"`
}

type ListenerConfig struct {
	Address        string        `yaml:"address"`
	MaxConnections int           `yaml:"max_connections"`
	ReadTimeout    time.Duration `yaml:"read_timeout"`
	WriteTimeout   time.Duration `yaml:"write_timeout"`
}

type BackendConfig struct {
	Primary  BackendNode   `yaml:"primary"`
	Replicas []BackendNode `yaml:"replicas"`
}

type BackendNode struct {
	Address string `yaml:"address"`
}

type PoolConfig struct {
	PrimarySize int `yaml:"primary_size"`
	ReplicaSize int `yaml:"replica_size"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}