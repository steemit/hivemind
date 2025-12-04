package logging

import (
	"encoding/json"
	"time"

	"go.uber.org/zap/buffer"
	"go.uber.org/zap/zapcore"
)

// ScalyrEncoder is a custom Zap encoder that outputs Scalyr-compatible JSON format
type ScalyrEncoder struct {
	zapcore.Encoder
	config zapcore.EncoderConfig
}

// NewScalyrEncoder creates a new Scalyr-compatible encoder
func NewScalyrEncoder(config zapcore.EncoderConfig) zapcore.Encoder {
	return &ScalyrEncoder{
		Encoder: zapcore.NewJSONEncoder(config),
		config:  config,
	}
}

// EncodeEntry encodes a log entry in Scalyr-compatible format
func (e *ScalyrEncoder) EncodeEntry(entry zapcore.Entry, fields []zapcore.Field) (*buffer.Buffer, error) {
	// Create Scalyr-compatible log object
	logObj := map[string]interface{}{
		"timestamp": entry.Time.Format(time.RFC3339Nano),
		"level":     entry.Level.String(),
		"message":   entry.Message,
		"logger":    entry.LoggerName,
	}

	// Add caller information if available
	if entry.Caller.Defined {
		logObj["file"] = entry.Caller.File
		logObj["line"] = entry.Caller.Line
		logObj["function"] = entry.Caller.Function
	}

	// Add stack trace if available
	if entry.Stack != "" {
		logObj["stack"] = entry.Stack
	}

	// Add fields
	fieldMap := make(map[string]interface{})
	for _, field := range fields {
		switch field.Type {
		case zapcore.StringType:
			fieldMap[field.Key] = field.String
		case zapcore.Int64Type, zapcore.Int32Type, zapcore.Int16Type, zapcore.Int8Type:
			fieldMap[field.Key] = field.Integer
		case zapcore.Uint64Type, zapcore.Uint32Type, zapcore.Uint16Type, zapcore.Uint8Type:
			fieldMap[field.Key] = field.Integer
		case zapcore.Float64Type, zapcore.Float32Type:
			fieldMap[field.Key] = field.Interface
		case zapcore.BoolType:
			fieldMap[field.Key] = field.Integer == 1
		case zapcore.DurationType:
			fieldMap[field.Key] = field.Interface.(time.Duration).String()
		case zapcore.TimeType:
			fieldMap[field.Key] = field.Interface.(time.Time).Format(time.RFC3339Nano)
		case zapcore.ErrorType:
			fieldMap[field.Key] = field.Interface.(error).Error()
		default:
			// For complex types, use the interface value
			fieldMap[field.Key] = field.Interface
		}
	}

	// Merge fields into log object
	for k, v := range fieldMap {
		logObj[k] = v
	}

	// Encode to JSON
	buf := buffer.NewPool().Get()
	encoder := json.NewEncoder(buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(logObj); err != nil {
		return nil, err
	}

	// Remove trailing newline (json.Encoder adds it)
	// Create a new buffer without the trailing newline
	if buf.Len() > 0 && buf.Bytes()[buf.Len()-1] == '\n' {
		data := buf.Bytes()[:buf.Len()-1]
		newBuf := buffer.NewPool().Get()
		newBuf.AppendBytes(data)
		return newBuf, nil
	}

	return buf, nil
}

// Clone creates a copy of the encoder
func (e *ScalyrEncoder) Clone() zapcore.Encoder {
	return &ScalyrEncoder{
		Encoder: e.Encoder.Clone(),
		config:  e.config,
	}
}

