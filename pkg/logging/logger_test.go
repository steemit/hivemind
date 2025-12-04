package logging

import (
	"bytes"
	"encoding/json"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"github.com/steemit/hivemind/pkg/config"
)

func TestScalyrEncoder(t *testing.T) {
	cfg := &config.LoggingConfig{
		Level:        "INFO",
		Format:       "json",
		ScalyrFormat: true,
	}

	err := InitLogger(cfg)
	if err != nil {
		t.Fatalf("Failed to initialize logger: %v", err)
	}

	// Capture output
	var buf bytes.Buffer
	oldLogger := Logger
	defer func() { Logger = oldLogger }()

	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		MessageKey:     "message",
		CallerKey:      "caller",
		StacktraceKey:  "stacktrace",
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	encoder := NewScalyrEncoder(encoderConfig)
	core := zapcore.NewCore(encoder, zapcore.AddSync(&buf), zapcore.InfoLevel)
	logger := zap.New(core)

	logger.Info("test message", zap.String("key", "value"))

	// Verify JSON output
	var logObj map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &logObj); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if logObj["message"] != "test message" {
		t.Errorf("Expected message 'test message', got: %v", logObj["message"])
	}

	if logObj["key"] != "value" {
		t.Errorf("Expected field 'key'='value', got: %v", logObj["key"])
	}

	if _, ok := logObj["timestamp"]; !ok {
		t.Error("Expected 'timestamp' field in log output")
	}
}

