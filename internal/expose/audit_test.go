package expose

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendIsMetadataOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	err := Append(path, Event{
		At:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Guest:  "alice",
		Action: "ask",
		Result: "ok",
		Bytes:  12,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"text", "path", "token"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("audit has %s", key)
		}
	}
	if strings.Contains(string(body), "hello") {
		t.Fatal("audit stored message text")
	}
}
