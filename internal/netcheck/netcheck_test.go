package netcheck

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRating(t *testing.T) {
	tests := []struct {
		name  string
		value Measurement
		want  string
	}{
		{"excellent", Measurement{DownloadMbps: 100, UploadMbps: 30, LatencyMs: 20, JitterMs: 3}, "excellent"},
		{"good", Measurement{DownloadMbps: 25, UploadMbps: 8, LatencyMs: 60, JitterMs: 12}, "good"},
		{"fair", Measurement{DownloadMbps: 8, UploadMbps: 3, LatencyMs: 130, JitterMs: 20}, "fair"},
		{"poor", Measurement{DownloadMbps: 2, UploadMbps: 1, LatencyMs: 300, JitterMs: 80}, "poor"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Rating(test.value); got != test.want {
				t.Fatalf("Rating() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestHistoryIsBoundedAndClearable(t *testing.T) {
	history := NewHistory(filepath.Join(t.TempDir(), "netchecks.json"))
	now := time.Now().UTC()
	for index := 0; index < 25; index++ {
		finished := now.Add(-time.Duration(index) * time.Hour).Format(time.RFC3339)
		if err := history.Add(Result{ID: finished, FinishedAt: finished}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := history.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 20 {
		t.Fatalf("history length = %d, want 20", len(items))
	}
	if items[0].FinishedAt < items[len(items)-1].FinishedAt {
		t.Fatal("history is not newest first")
	}
	if err := history.Clear(); err != nil {
		t.Fatal(err)
	}
	items, err = history.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("history length after clear = %d", len(items))
	}
}

func TestHistoryPrunesOldEntries(t *testing.T) {
	history := NewHistory(filepath.Join(t.TempDir(), "netchecks.json"))
	old := time.Now().UTC().Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	recent := time.Now().UTC().Format(time.RFC3339)
	if err := history.Add(Result{ID: "old", FinishedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := history.Add(Result{ID: "recent", FinishedAt: recent}); err != nil {
		t.Fatal(err)
	}
	items, err := history.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "recent" {
		t.Fatalf("unexpected pruned history: %#v", items)
	}
}

func TestCompare(t *testing.T) {
	result := Result{Results: []Measurement{
		{Route: "direct", DownloadMbps: 100, UploadMbps: 20, LatencyMs: 20},
		{Route: "proxy", DownloadMbps: 75, UploadMbps: 10, LatencyMs: 55},
	}}
	Compare(&result)
	if result.Compare == nil || result.Compare.DownloadDeltaPct != -25 ||
		result.Compare.UploadDeltaPct != -50 || result.Compare.LatencyDeltaMs != 35 {
		t.Fatalf("unexpected comparison: %#v", result.Compare)
	}
}
