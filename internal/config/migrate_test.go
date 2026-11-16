package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateDataDirMovesLegacyDirectory(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, legacyDataDirName)
	current := filepath.Join(root, DataDirName)

	if err := os.MkdirAll(filepath.Join(legacy, "sessions"), 0o755); err != nil {
		t.Fatalf("prepare legacy sessions dir: %v", err)
	}
	const payload = `{"model":"deepseek-chat"}`
	if err := os.WriteFile(filepath.Join(legacy, "config.json"), []byte(payload), 0o644); err != nil {
		t.Fatalf("prepare legacy config: %v", err)
	}

	migrated, err := migrateDataDir(legacy, current)
	if err != nil {
		t.Fatalf("migrateDataDir: %v", err)
	}
	if !migrated {
		t.Fatal("migrated = false, want true")
	}
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy directory still present: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(current, "config.json"))
	if err != nil {
		t.Fatalf("read migrated config: %v", err)
	}
	if string(data) != payload {
		t.Fatalf("migrated config = %q, want %q", data, payload)
	}
	if _, err := os.Stat(filepath.Join(current, "sessions")); err != nil {
		t.Fatalf("sessions directory not migrated: %v", err)
	}
}

func TestMigrateDataDirKeepsBothWhenCurrentExists(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, legacyDataDirName)
	current := filepath.Join(root, DataDirName)
	for _, dir := range []string{legacy, current} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o644); err != nil {
			t.Fatalf("write config in %s: %v", dir, err)
		}
	}

	migrated, err := migrateDataDir(legacy, current)
	if err != nil {
		t.Fatalf("migrateDataDir: %v", err)
	}
	if migrated {
		t.Fatal("migrated = true, want false when the current directory already exists")
	}
	for _, dir := range []string{legacy, current} {
		if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
			t.Fatalf("config in %s was touched: %v", dir, err)
		}
	}
}

func TestMigrateDataDirNoopWithoutLegacyDirectory(t *testing.T) {
	root := t.TempDir()

	migrated, err := migrateDataDir(filepath.Join(root, legacyDataDirName), filepath.Join(root, DataDirName))
	if err != nil {
		t.Fatalf("migrateDataDir: %v", err)
	}
	if migrated {
		t.Fatal("migrated = true, want false when there is nothing to migrate")
	}
	if _, err := os.Stat(filepath.Join(root, DataDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("current directory must not be created: %v", err)
	}
}

func TestMigrateLegacyDataDirResolvesHomeAndRunsOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, legacyDataDirName), 0o755); err != nil {
		t.Fatalf("prepare legacy dir: %v", err)
	}

	migrated, err := MigrateLegacyDataDir()
	if err != nil {
		t.Fatalf("MigrateLegacyDataDir: %v", err)
	}
	if !migrated {
		t.Fatal("migrated = false, want true")
	}
	if _, err := os.Stat(filepath.Join(home, DataDirName)); err != nil {
		t.Fatalf("current data directory missing: %v", err)
	}

	again, err := MigrateLegacyDataDir()
	if err != nil {
		t.Fatalf("second MigrateLegacyDataDir: %v", err)
	}
	if again {
		t.Fatal("second migration reported a change, want a no-op")
	}
}
