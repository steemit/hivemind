package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// TestScalyrEncoderFieldTypes pins the PR#430 regression fix: zap packs
// Duration/Time/float values into the Integer slot and leaves Interface
// nil, so the encoder must not type-assert Interface for those types —
// before the fix the first zap.Duration field panicked the process
// (observed as a startup crash of the server binary under the default
// HIVE_LOG_SCALYR_FORMAT=true).
func TestScalyrEncoderFieldTypes(t *testing.T) {
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:     "timestamp",
		LevelKey:    "level",
		MessageKey:  "message",
		EncodeTime:  zapcore.ISO8601TimeEncoder,
		EncodeLevel: zapcore.LowercaseLevelEncoder,
	}
	var buf bytes.Buffer
	core := zapcore.NewCore(NewScalyrEncoder(encoderConfig), zapcore.AddSync(&buf), zapcore.InfoLevel)
	logger := zap.New(core)

	ts := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	logger.Info("field matrix",
		zap.Duration("dur", 90*time.Second),
		zap.Time("ts", ts),
		zap.Float64("f64", 1.5),
		zap.Float32("f32", 0.25),
		zap.Bool("flag", true),
		zap.Int("count", 7),
		zap.Error(errors.New("boom")),
	)

	var obj map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &obj); err != nil {
		t.Fatalf("parse output %q: %v", buf.String(), err)
	}
	if got := obj["dur"]; got != "1m30s" {
		t.Errorf("dur = %v, want 1m30s", got)
	}
	if got, ok := obj["ts"].(string); !ok || got[:10] != "2026-10-14" {
		t.Errorf("ts = %v, want RFC3339 of 2026-10-14", obj["ts"])
	}
	if got := obj["f64"]; got != float64(1.5) {
		t.Errorf("f64 = %v (%T), want 1.5", got, got)
	}
	if got := obj["f32"]; got != float64(float32(0.25)) {
		t.Errorf("f32 = %v (%T), want 0.25", got, got)
	}
	if got := obj["flag"]; got != true {
		t.Errorf("flag = %v, want true", got)
	}
	if got := obj["count"]; got != float64(7) {
		t.Errorf("count = %v, want 7", got)
	}
	if got := obj["error"]; got != "boom" {
		t.Errorf("error = %v, want boom", got)
	}
}
