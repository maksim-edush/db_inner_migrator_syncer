package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ScriptRecord describes a migration script stored on disk.
type ScriptRecord struct {
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	ForwardFile  string    `json:"forward_file"`
	RollbackFile string    `json:"rollback_file"`
	CreatedAt    time.Time `json:"created_at"`
	Checksum     string    `json:"checksum"`
}

// EnsureBase makes sure the storage root exists.
func EnsureBase(base string) error {
	return os.MkdirAll(filepath.Join(base, "scripts"), 0o755)
}

// StoreScript copies the provided forward (and optional rollback) scripts into storage.
func StoreScript(base, pair, name, forwardPath, rollbackPath, description string) (ScriptRecord, error) {
	if pair == "" || name == "" {
		return ScriptRecord{}, fmt.Errorf("pair and name are required")
	}
	dir := filepath.Join(base, "scripts", safeName(pair), safeName(name))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ScriptRecord{}, err
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	if _, err := os.Stat(manifestPath); err == nil {
		return ScriptRecord{}, fmt.Errorf("script %s for pair %s already exists", name, pair)
	}

	forwardBytes, err := os.ReadFile(forwardPath)
	if err != nil {
		return ScriptRecord{}, fmt.Errorf("read forward script: %w", err)
	}
	forwardTarget := filepath.Join(dir, "forward.sql")
	if err := os.WriteFile(forwardTarget, forwardBytes, 0o644); err != nil {
		return ScriptRecord{}, fmt.Errorf("write forward script: %w", err)
	}

	record := ScriptRecord{
		Name:        name,
		Description: description,
		ForwardFile: forwardTarget,
		CreatedAt:   time.Now().UTC(),
	}

	if rollbackPath != "" {
		rollbackBytes, err := os.ReadFile(rollbackPath)
		if err != nil {
			return ScriptRecord{}, fmt.Errorf("read rollback script: %w", err)
		}
		rollbackTarget := filepath.Join(dir, "rollback.sql")
		if err := os.WriteFile(rollbackTarget, rollbackBytes, 0o644); err != nil {
			return ScriptRecord{}, fmt.Errorf("write rollback script: %w", err)
		}
		record.RollbackFile = rollbackTarget
		record.Checksum = computeChecksum(forwardBytes, rollbackBytes)
	} else {
		record.Checksum = computeChecksum(forwardBytes)
	}

	if err := writeJSON(manifestPath, record); err != nil {
		return ScriptRecord{}, err
	}
	return record, nil
}

// StoreScriptContent writes raw SQL strings into storage (used by the web UI).
func StoreScriptContent(base, pair, name, forwardSQL, rollbackSQL, description string) (ScriptRecord, error) {
	tmpDir, err := os.MkdirTemp("", "migrator-content-*")
	if err != nil {
		return ScriptRecord{}, err
	}
	defer os.RemoveAll(tmpDir)

	fwdPath := filepath.Join(tmpDir, "forward.sql")
	if err := os.WriteFile(fwdPath, []byte(forwardSQL), 0o644); err != nil {
		return ScriptRecord{}, err
	}
	var rbPath string
	if rollbackSQL != "" {
		rbPath = filepath.Join(tmpDir, "rollback.sql")
		if err := os.WriteFile(rbPath, []byte(rollbackSQL), 0o644); err != nil {
			return ScriptRecord{}, err
		}
	}
	return StoreScript(base, pair, name, fwdPath, rbPath, description)
}

// LoadScript reads a stored script record and file contents.
func LoadScript(base, pair, name string) (ScriptRecord, string, string, error) {
	dir := filepath.Join(base, "scripts", safeName(pair), safeName(name))
	manifestPath := filepath.Join(dir, "manifest.json")
	var record ScriptRecord
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return record, "", "", fmt.Errorf("read manifest: %w", err)
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, "", "", fmt.Errorf("parse manifest: %w", err)
	}

	forwardBytes, err := os.ReadFile(record.ForwardFile)
	if err != nil {
		return record, "", "", fmt.Errorf("read forward script: %w", err)
	}
	var rollbackContent string
	if record.RollbackFile != "" {
		rb, err := os.ReadFile(record.RollbackFile)
		if err != nil {
			return record, "", "", fmt.Errorf("read rollback script: %w", err)
		}
		rollbackContent = string(rb)
	}
	return record, string(forwardBytes), rollbackContent, nil
}

// LoadManifest reads metadata without loading script bodies.
func LoadManifest(base, pair, name string) (ScriptRecord, error) {
	dir := filepath.Join(base, "scripts", safeName(pair), safeName(name))
	manifestPath := filepath.Join(dir, "manifest.json")
	var record ScriptRecord
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return record, fmt.Errorf("read manifest: %w", err)
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, fmt.Errorf("parse manifest: %w", err)
	}
	return record, nil
}

// ListScripts returns stored script names for a pair.
func ListScripts(base, pair string) ([]string, error) {
	dir := filepath.Join(base, "scripts", safeName(pair))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// ListScriptRecords returns manifest details for scripts under a pair.
func ListScriptRecords(base, pair string) ([]ScriptRecord, error) {
	names, err := ListScripts(base, pair)
	if err != nil {
		return nil, err
	}
	records := make([]ScriptRecord, 0, len(names))
	for _, name := range names {
		rec, err := LoadManifest(base, pair, name)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, nil
}

func safeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, " ", "_")
	return name
}

func computeChecksum(blobs ...[]byte) string {
	h := sha256.New()
	for _, b := range blobs {
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
