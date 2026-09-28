package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"weave-os/router/internal/trafficcapture"
)

const (
	trafficCaptureFileEnv              = "ROUTER_HTTP_CAPTURE_FILE"
	trafficCaptureSensitiveHeadersEnv  = "ROUTER_HTTP_CAPTURE_SENSITIVE_HEADERS"
	trafficCaptureFilePermissions      = 0o600
	trafficCaptureDirectoryPermissions = 0o700
)

type jsonlTrafficCapture struct {
	file             *os.File
	encoder          *json.Encoder
	includeSensitive bool
	mu               sync.Mutex
	closed           bool
}

func newTrafficCaptureFromEnvironment() (*jsonlTrafficCapture, error) {
	path := strings.TrimSpace(os.Getenv(trafficCaptureFileEnv))
	if path == "" {
		if strings.EqualFold(os.Getenv(trafficCaptureSensitiveHeadersEnv), "true") {
			return nil, fmt.Errorf("%s requires %s", trafficCaptureSensitiveHeadersEnv, trafficCaptureFileEnv)
		}
		return nil, nil
	}

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, trafficCaptureDirectoryPermissions); err != nil {
		return nil, fmt.Errorf("create HTTP capture directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, trafficCaptureFilePermissions)
	if err != nil {
		return nil, fmt.Errorf("open HTTP capture file: %w", err)
	}
	if err := file.Chmod(trafficCaptureFilePermissions); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("restrict HTTP capture file permissions: %w", err)
	}
	return &jsonlTrafficCapture{
		file:             file,
		encoder:          json.NewEncoder(file),
		includeSensitive: strings.EqualFold(os.Getenv(trafficCaptureSensitiveHeadersEnv), "true"),
	}, nil
}

func (capture *jsonlTrafficCapture) Record(exchange trafficcapture.Exchange) error {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.closed {
		return fmt.Errorf("HTTP capture file is closed")
	}
	if !capture.includeSensitive {
		exchange.Request.Header = redactCaptureHeaders(exchange.Request.Header)
		if exchange.Response != nil {
			exchange.Response.Header = redactCaptureHeaders(exchange.Response.Header)
		}
	}
	if err := capture.encoder.Encode(exchange); err != nil {
		return fmt.Errorf("encode HTTP capture exchange: %w", err)
	}
	if err := capture.file.Sync(); err != nil {
		return fmt.Errorf("sync HTTP capture exchange: %w", err)
	}
	return nil
}

func (capture *jsonlTrafficCapture) Close() error {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.closed {
		return nil
	}
	capture.closed = true
	if err := capture.file.Sync(); err != nil {
		_ = capture.file.Close()
		return fmt.Errorf("sync HTTP capture file on close: %w", err)
	}
	if err := capture.file.Close(); err != nil {
		return fmt.Errorf("close HTTP capture file: %w", err)
	}
	return nil
}

func redactCaptureHeaders(headers map[string][]string) map[string][]string {
	redacted := make(map[string][]string, len(headers))
	for name, values := range headers {
		if !isSafeCaptureHeader(name) {
			redacted[name] = []string{"[REDACTED]"}
			continue
		}
		redacted[name] = append([]string(nil), values...)
	}
	return redacted
}

func isSafeCaptureHeader(name string) bool {
	switch strings.ToLower(name) {
	case "accept", "accept-encoding", "anthropic-beta", "anthropic-version", "content-encoding", "content-length", "content-type", "openai-beta", "transfer-encoding", "user-agent", "x-goog-api-client":
		return true
	default:
		return false
	}
}
