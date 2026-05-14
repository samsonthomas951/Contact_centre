package config

import (
	"strings"
	"testing"
	"time"
)

type sample struct {
	Host    string        `env:"HOST" required:"true"`
	Port    int           `env:"PORT" default:"8080"`
	Timeout time.Duration `env:"TIMEOUT" default:"30s"`
	Debug   bool          `env:"DEBUG" default:"false"`
}

func TestLoad_DefaultsAndOverrides(t *testing.T) {
	t.Setenv("APP_HOST", "0.0.0.0")
	t.Setenv("APP_DEBUG", "true")

	var s sample
	if err := Load("app", &s); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Host != "0.0.0.0" {
		t.Errorf("Host = %q, want 0.0.0.0", s.Host)
	}
	if s.Port != 8080 {
		t.Errorf("Port = %d, want 8080 (default)", s.Port)
	}
	if s.Timeout != 30*time.Second {
		t.Errorf("Timeout = %s, want 30s (default)", s.Timeout)
	}
	if !s.Debug {
		t.Errorf("Debug = false, want true")
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	var s sample
	err := Load("app", &s)
	if err == nil {
		t.Fatal("expected error for missing APP_HOST")
	}
	if !strings.Contains(err.Error(), "APP_HOST") {
		t.Errorf("error should mention APP_HOST, got %q", err)
	}
}
