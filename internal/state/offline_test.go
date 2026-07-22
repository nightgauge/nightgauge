package state

import (
	"testing"
)

func TestOfflineSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewOfflineStore(dir)

	state := &OfflineState{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 1311,
		ItemID:      "item-123",
		Stage:       StageFeatureDev,
		Status:      StatusInProgress,
	}

	if err := store.Save(state); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load(1311)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil")
	}
	if loaded.Stage != StageFeatureDev {
		t.Errorf("Stage = %q, want %q", loaded.Stage, StageFeatureDev)
	}
	if loaded.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set")
	}
}

func TestOfflineLoadMissing(t *testing.T) {
	dir := t.TempDir()
	store := NewOfflineStore(dir)

	loaded, err := store.Load(9999)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded != nil {
		t.Error("should return nil for missing state")
	}
}

func TestOfflineRemove(t *testing.T) {
	dir := t.TempDir()
	store := NewOfflineStore(dir)

	state := &OfflineState{
		Repo:        "nightgauge/nightgauge",
		IssueNumber: 1311,
		ItemID:      "item-123",
		Stage:       StageFeatureDev,
		Status:      StatusInProgress,
	}
	if err := store.Save(state); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.Remove(1311); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	loaded, _ := store.Load(1311)
	if loaded != nil {
		t.Error("should be nil after remove")
	}
}
