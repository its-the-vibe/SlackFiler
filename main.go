package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"github.com/slack-go/slack"
	"gopkg.in/yaml.v3"
)

// Config holds the application configuration loaded from config.yaml.
type Config struct {
	Redis struct {
		Host       string `yaml:"host"`
		Port       int    `yaml:"port"`
		InputList  string `yaml:"input_list"`
		OutputList string `yaml:"output_list"`
	} `yaml:"redis"`
	// Channels maps a Slack channel ID to a local filesystem directory.
	Channels map[string]string `yaml:"channels"`
}

// SlackEvent represents the outer envelope of a Slack event_callback payload.
type SlackEvent struct {
	Type  string          `json:"type"`
	Event json.RawMessage `json:"event"`
}

// FileSharedEvent represents the inner event for a file_shared notification.
type FileSharedEvent struct {
	Type      string `json:"type"`
	FileID    string `json:"file_id"`
	ChannelID string `json:"channel_id"`
}

// ResultMessage is the payload pushed to the Redis output list after a
// successful file download.
type ResultMessage struct {
	FileInfo       *slack.File `json:"file_info"`
	TargetFilePath string      `json:"target_file_path"`
}

func loadConfig(path string) (*Config, error) {
	f, err := os.Open(path) // #nosec G304 – path is operator-supplied via flag/env
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()

	var cfg Config
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	return &cfg, nil
}

func main() {
	// Load .env when present (useful in local development; Docker Compose uses
	// env_file which sets vars before the process starts, so this is a no-op
	// in production).
	_ = godotenv.Load()

	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config.yaml"
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	slackToken := os.Getenv("SLACK_BOT_TOKEN")
	if slackToken == "" {
		log.Fatal("SLACK_BOT_TOKEN environment variable is required")
	}

	redisPassword := os.Getenv("REDIS_PASSWORD")

	rdb := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", cfg.Redis.Host, cfg.Redis.Port),
		Password: redisPassword,
	})

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("connect to Redis: %v", err)
	}
	log.Printf("connected to Redis at %s:%d", cfg.Redis.Host, cfg.Redis.Port)

	slackClient := slack.New(slackToken)

	log.Printf("polling Redis list %q for file_shared events", cfg.Redis.InputList)
	for {
		result, err := rdb.LPop(ctx, cfg.Redis.InputList).Result()
		if err == redis.Nil {
			time.Sleep(time.Second)
			continue
		}
		if err != nil {
			log.Printf("LPOP error: %v", err)
			time.Sleep(time.Second)
			continue
		}

		if err := processEvent(ctx, result, cfg, slackToken, slackClient, rdb); err != nil {
			log.Printf("process event error: %v", err)
		}
	}
}

func processEvent(ctx context.Context, raw string, cfg *Config, slackToken string, slackClient *slack.Client, rdb *redis.Client) error {
	var envelope SlackEvent
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return fmt.Errorf("unmarshal event envelope: %w", err)
	}

	if envelope.Type != "event_callback" {
		log.Printf("skipping event type %q", envelope.Type)
		return nil
	}

	var fileEvent FileSharedEvent
	if err := json.Unmarshal(envelope.Event, &fileEvent); err != nil {
		return fmt.Errorf("unmarshal inner event: %w", err)
	}

	if fileEvent.Type != "file_shared" {
		log.Printf("skipping inner event type %q", fileEvent.Type)
		return nil
	}

	targetDir, ok := cfg.Channels[fileEvent.ChannelID]
	if !ok {
		log.Printf("channel %q is not configured – skipping", fileEvent.ChannelID)
		return nil
	}

	fileInfo, _, _, err := slackClient.GetFileInfoContext(ctx, fileEvent.FileID, 0, 0)
	if err != nil {
		return fmt.Errorf("GetFileInfo %s: %w", fileEvent.FileID, err)
	}

	targetPath := filepath.Join(targetDir, fileInfo.Name)

	if err := downloadFile(ctx, slackToken, fileInfo, targetPath); err != nil {
		return fmt.Errorf("download file: %w", err)
	}

	log.Printf("saved file %q to %q", fileInfo.Name, targetPath)

	resultMsg := ResultMessage{
		FileInfo:       fileInfo,
		TargetFilePath: targetPath,
	}
	resultJSON, err := json.Marshal(resultMsg)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	if err := rdb.RPush(ctx, cfg.Redis.OutputList, string(resultJSON)).Err(); err != nil {
		return fmt.Errorf("RPUSH result: %w", err)
	}

	return nil
}

// downloadFile fetches the Slack file and writes it to targetPath, creating
// parent directories as needed.
func downloadFile(ctx context.Context, slackToken string, fileInfo *slack.File, targetPath string) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create target directory: %w", err)
	}

	// Use the Slack SDK's download helper which handles authentication.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileInfo.URLPrivateDownload, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+slackToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected download status: %s", resp.Status)
	}

	// Write to a temporary file in the same directory, then rename atomically.
	tmpFile, err := os.CreateTemp(filepath.Dir(targetPath), ".slackfiler-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpPath) // no-op if rename succeeded
	}()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}
