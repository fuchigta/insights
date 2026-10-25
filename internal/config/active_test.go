package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// judge.active_grace の既定値（30 分）が往復し、雛形に説明のコメントが付くことを確かめる。
func TestActiveGrace_DefaultAndTemplate(t *testing.T) {
	if got := Default().Judge.ActiveGrace.Duration; got != 30*time.Minute {
		t.Errorf("既定の ActiveGrace = %s, want 30m", got)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := SaveTemplate(path); err != nil {
		t.Fatalf("SaveTemplate() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), "作業中") || !strings.Contains(string(data), "active_grace: 30m0s") {
		t.Errorf("雛形に active_grace とそのコメントが無い:\n%s", data)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Judge.ActiveGrace.Duration != 30*time.Minute {
		t.Errorf("往復後の ActiveGrace = %s, want 30m", got.Judge.ActiveGrace.Duration)
	}
}
