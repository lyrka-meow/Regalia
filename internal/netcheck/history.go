package netcheck

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	maxHistory = 20
	maxAge     = 30 * 24 * time.Hour
)

type History struct {
	path string
	mu   sync.Mutex
}

func NewHistory(path string) *History {
	return &History{path: path}
}

func (history *History) List() ([]Result, error) {
	history.mu.Lock()
	defer history.mu.Unlock()
	results, changed, err := history.readLocked()
	if err != nil {
		return nil, err
	}
	if changed {
		if err := history.writeLocked(results); err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (history *History) Add(result Result) error {
	history.mu.Lock()
	defer history.mu.Unlock()
	results, _, err := history.readLocked()
	if err != nil {
		return err
	}
	results = append([]Result{result}, results...)
	results, _ = prune(results)
	return history.writeLocked(results)
}

func (history *History) Clear() error {
	history.mu.Lock()
	defer history.mu.Unlock()
	return history.writeLocked([]Result{})
}

func (history *History) readLocked() ([]Result, bool, error) {
	raw, err := os.ReadFile(history.path)
	if errors.Is(err, os.ErrNotExist) {
		return []Result{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var document struct {
		Version int      `json:"version"`
		Tests   []Result `json:"tests"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, false, err
	}
	results, changed := prune(document.Tests)
	return results, changed || document.Version != 1, nil
}

func prune(results []Result) ([]Result, bool) {
	originalLength := len(results)
	cutoff := time.Now().Add(-maxAge)
	filtered := make([]Result, 0, len(results))
	for _, result := range results {
		finished, err := time.Parse(time.RFC3339, result.FinishedAt)
		if err == nil && !finished.Before(cutoff) && result.ID != "" {
			filtered = append(filtered, result)
		}
	}
	sort.SliceStable(filtered, func(left, right int) bool {
		return filtered[left].FinishedAt > filtered[right].FinishedAt
	})
	if len(filtered) > maxHistory {
		filtered = filtered[:maxHistory]
	}
	return filtered, len(filtered) != originalLength
}

func (history *History) writeLocked(results []Result) error {
	if err := os.MkdirAll(filepath.Dir(history.path), 0o700); err != nil {
		return err
	}
	document := struct {
		Version int      `json:"version"`
		Tests   []Result `json:"tests"`
	}{Version: 1, Tests: results}
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(history.path), ".netchecks-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, history.path)
}
