package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	yamlContent := `
listener:
  address: ":5432"
  max_connections: 100
  read_timeout: 30s
  write_timeout: 30s
backend:
  primary:
    address: "localhost:5433"
  replicas:
    - address: "localhost:5434"
    - address: "localhost:5435"
pool:
  primary_size: 20
  replica_size: 10
`
	tmpfile, err := os.CreateTemp("", "config*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpfile.Name())

	if _, err := tmpfile.Write([]byte(yamlContent)); err != nil {
		t.Fatal(err)
	}
	if err := tmpfile.Close(); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tmpfile.Name())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Listener.Address != ":5432" {
		t.Errorf("cfg.Listener.Address = %v, want %v", cfg.Listener.Address, ":5432")
	}
	if cfg.Listener.MaxConnections != 100 {
		t.Errorf("cfg.Listener.MaxConnections = %v, want %v", cfg.Listener.MaxConnections, 100)
	}
	if cfg.Listener.ReadTimeout != 30*time.Second {
		t.Errorf("cfg.Listener.ReadTimeout = %v, want %v", cfg.Listener.ReadTimeout, 30*time.Second)
	}
	if cfg.Backend.Primary.Address != "localhost:5433" {
		t.Errorf("cfg.Backend.Primary.Address = %v, want %v", cfg.Backend.Primary.Address, "localhost:5433")
	}
	if len(cfg.Backend.Replicas) != 2 {
		t.Errorf("len(cfg.Backend.Replicas) = %v, want %v", len(cfg.Backend.Replicas), 2)
	}
	if cfg.Pool.PrimarySize != 20 {
		t.Errorf("cfg.Pool.PrimarySize = %v, want %v", cfg.Pool.PrimarySize, 20)
	}
	if cfg.Pool.ReplicaSize != 10 {
		t.Errorf("cfg.Pool.ReplicaSize = %v, want %v", cfg.Pool.ReplicaSize, 10)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("non_existent_file.yaml")
	if err == nil {
		t.Error("Load() expected error for non-existent file, got nil")
	}
}
