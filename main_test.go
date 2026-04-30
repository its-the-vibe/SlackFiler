package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/slack-go/slack"
)

// ---------------------------------------------------------------------------
// loadConfig tests
// ---------------------------------------------------------------------------

func TestLoadConfig_Valid(t *testing.T) {
	content := `
redis:
  host: "localhost"
  port: 6379
  input_list: "events"
  output_list: "results"
channels:
  C123: "/tmp/general"
`
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg.Redis.Host != "localhost" {
		t.Errorf("Redis.Host: got %q, want %q", cfg.Redis.Host, "localhost")
	}
	if cfg.Redis.Port != 6379 {
		t.Errorf("Redis.Port: got %d, want %d", cfg.Redis.Port, 6379)
	}
	if cfg.Redis.InputList != "events" {
		t.Errorf("Redis.InputList: got %q, want %q", cfg.Redis.InputList, "events")
	}
	if cfg.Redis.OutputList != "results" {
		t.Errorf("Redis.OutputList: got %q, want %q", cfg.Redis.OutputList, "results")
	}
	if dir, ok := cfg.Channels["C123"]; !ok || dir != "/tmp/general" {
		t.Errorf("Channels[C123]: got %q ok=%v, want %q", dir, ok, "/tmp/general")
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := loadConfig("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte("redis:\n\thost: localhost"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfig(configPath)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

// ---------------------------------------------------------------------------
// processEvent tests (paths that do not reach the Slack / Redis clients)
// ---------------------------------------------------------------------------

func makeTestConfig(channels map[string]string) *Config {
	cfg := &Config{}
	cfg.Redis.Host = "localhost"
	cfg.Redis.Port = 6379
	cfg.Redis.InputList = "in"
	cfg.Redis.OutputList = "out"
	cfg.Channels = channels
	return cfg
}

func TestProcessEvent_InvalidJSON(t *testing.T) {
	cfg := makeTestConfig(nil)
	err := processEvent(context.Background(), "not-json", cfg, "token", nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON envelope, got nil")
	}
}

func TestProcessEvent_NonEventCallback(t *testing.T) {
	cfg := makeTestConfig(nil)
	raw, _ := json.Marshal(SlackEvent{Type: "url_verification"})
	err := processEvent(context.Background(), string(raw), cfg, "token", nil, nil)
	if err != nil {
		t.Fatalf("expected nil for non-event_callback type, got: %v", err)
	}
}

func TestProcessEvent_InvalidInnerJSON(t *testing.T) {
	cfg := makeTestConfig(nil)
	evt := SlackEvent{
		Type:  "event_callback",
		Event: json.RawMessage(`"not-an-object"`),
	}
	raw, _ := json.Marshal(evt)
	err := processEvent(context.Background(), string(raw), cfg, "token", nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid inner event JSON, got nil")
	}
}

func TestProcessEvent_NonFileShared(t *testing.T) {
	cfg := makeTestConfig(nil)
	inner, _ := json.Marshal(FileSharedEvent{Type: "message"})
	evt := SlackEvent{Type: "event_callback", Event: json.RawMessage(inner)}
	raw, _ := json.Marshal(evt)
	err := processEvent(context.Background(), string(raw), cfg, "token", nil, nil)
	if err != nil {
		t.Fatalf("expected nil for non-file_shared inner event, got: %v", err)
	}
}

func TestProcessEvent_UnknownChannel(t *testing.T) {
	cfg := makeTestConfig(map[string]string{"C999": "/tmp/other"})
	inner, _ := json.Marshal(FileSharedEvent{Type: "file_shared", FileID: "F001", ChannelID: "C000"})
	evt := SlackEvent{Type: "event_callback", Event: json.RawMessage(inner)}
	raw, _ := json.Marshal(evt)
	err := processEvent(context.Background(), string(raw), cfg, "token", nil, nil)
	if err != nil {
		t.Fatalf("expected nil for unknown channel, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// downloadFile tests
// ---------------------------------------------------------------------------

func TestDownloadFile_Success(t *testing.T) {
	want := []byte("hello slack file content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(want)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	targetPath := filepath.Join(tmp, "test.txt")
	fileInfo := &slack.File{
		Name:               "test.txt",
		URLPrivateDownload: srv.URL + "/download",
	}

	if err := downloadFile(context.Background(), "test-token", fileInfo, targetPath); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("could not read downloaded file: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("file content: got %q, want %q", got, want)
	}
}

func TestDownloadFile_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	fileInfo := &slack.File{URLPrivateDownload: srv.URL + "/download"}
	err := downloadFile(context.Background(), "token", fileInfo, filepath.Join(tmp, "out.txt"))
	if err == nil {
		t.Fatal("expected error for HTTP 500 response, got nil")
	}
}

func TestDownloadFile_InvalidURL(t *testing.T) {
	tmp := t.TempDir()
	fileInfo := &slack.File{URLPrivateDownload: "://invalid-url"}
	err := downloadFile(context.Background(), "token", fileInfo, filepath.Join(tmp, "out.txt"))
	if err == nil {
		t.Fatal("expected error for invalid URL, got nil")
	}
}
