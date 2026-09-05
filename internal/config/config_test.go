package config

import (
	"path/filepath"
	"testing"
)

func TestLoadFromEnvUsesExplicitDataRootAndImageOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEGENBOX_DATA_DIR", root)
	t.Setenv("CODEGENBOX_IMAGE", "example.test/codegenbox:proof")

	configured, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if configured.DataRoot != filepath.Clean(root) {
		t.Fatalf("DataRoot = %q, want %q", configured.DataRoot, root)
	}
	if configured.Image != "example.test/codegenbox:proof" {
		t.Fatalf("Image = %q", configured.Image)
	}
	if configured.DockerBinary != "docker" {
		t.Fatalf("DockerBinary = %q, want docker", configured.DockerBinary)
	}
}

func TestLoadFromEnvUsesDefaultProductionImage(t *testing.T) {
	t.Setenv("CODEGENBOX_IMAGE", "")

	configured, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("LoadFromEnv: %v", err)
	}
	if configured.Image != DefaultImage {
		t.Fatalf("Image = %q, want default %q", configured.Image, DefaultImage)
	}
	if DefaultImage != "docker.io/atacandur/codegenbox:0.2.1" {
		t.Fatalf("DefaultImage = %q, want immutable production image tag", DefaultImage)
	}
}

func TestLoadFromEnvParsesOptionalResourceLimits(t *testing.T) {
	t.Setenv("CODEGENBOX_PIDS_LIMIT", "256"); t.Setenv("CODEGENBOX_MEMORY_LIMIT", "2g"); t.Setenv("CODEGENBOX_CPUS_LIMIT", "1.5")
	configured, err := LoadFromEnv(); if err != nil || configured.Limits.PIDs != 256 || configured.Limits.Memory != "2g" || configured.Limits.CPUs != "1.5" { t.Fatalf("limits = %#v, %v", configured.Limits, err) }
	t.Setenv("CODEGENBOX_PIDS_LIMIT", "0"); if _, err := LoadFromEnv(); err == nil { t.Fatal("zero pids limit accepted") }
}
