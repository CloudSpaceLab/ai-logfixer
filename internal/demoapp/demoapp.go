package demoapp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	ServiceName string `json:"service_name"`
	UpstreamURL string `json:"upstream_url"`
}

func ReadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return Config{}, err
	}

	var config Config
	if err := json.Unmarshal(raw, &config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func WriteConfig(path string, config Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	return os.WriteFile(filepath.Clean(path), raw, 0o644)
}

func NewHandler(configPath string, logPath string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok\n"))
	})

	mux.HandleFunc("/upstream/orders", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"orders":[{"id":"ord_001","status":"ready"}]}` + "\n"))
	})

	mux.HandleFunc("/orders", func(writer http.ResponseWriter, request *http.Request) {
		config, err := ReadConfig(configPath)
		if err != nil {
			appendLog(logPath, fmt.Sprintf("level=error service=unknown route=/orders status=503 error=%q", err.Error()))
			http.Error(writer, "configuration unavailable", http.StatusServiceUnavailable)
			return
		}

		client := http.Client{Timeout: 500 * time.Millisecond}
		response, err := client.Get(config.UpstreamURL)
		if err != nil {
			appendLog(logPath, fmt.Sprintf("level=error service=%s route=/orders status=503 upstream_url=%q error=%q", config.ServiceName, config.UpstreamURL, err.Error()))
			http.Error(writer, "upstream unavailable", http.StatusServiceUnavailable)
			return
		}
		defer response.Body.Close()

		if response.StatusCode >= http.StatusInternalServerError || response.StatusCode == http.StatusNotFound {
			appendLog(logPath, fmt.Sprintf("level=error service=%s route=/orders status=503 upstream_url=%q upstream_status=%d", config.ServiceName, config.UpstreamURL, response.StatusCode))
			http.Error(writer, "upstream returned an unhealthy response", http.StatusServiceUnavailable)
			return
		}

		appendLog(logPath, fmt.Sprintf("level=info service=%s route=/orders status=200 upstream_url=%q", config.ServiceName, config.UpstreamURL))
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"orders":[{"id":"ord_001","status":"ready"}]}` + "\n"))
	})

	return mux
}

func appendLog(path string, message string) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}

	line := time.Now().UTC().Format(time.RFC3339) + " " + strings.TrimSpace(message) + "\n"
	file, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString(line)
}
