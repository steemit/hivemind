package logging

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"github.com/steemit/hivemind/pkg/config"
)

// Logger is the application logger
var Logger *zap.Logger

// InitLogger initializes the logger with the given configuration
func InitLogger(cfg *config.LoggingConfig) error {
	var zapConfig zap.Config

	// Set log level
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(cfg.Level)); err != nil {
		level = zapcore.InfoLevel
	}

	if cfg.Format == "text" {
		// Development config for text format
		zapConfig = zap.NewDevelopmentConfig()
		zapConfig.Level = zap.NewAtomicLevelAt(level)
		zapConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	} else {
		// Production config for JSON format
		zapConfig = zap.NewProductionConfig()
		zapConfig.Level = zap.NewAtomicLevelAt(level)
		
		// Use Scalyr encoder if enabled
		if cfg.ScalyrFormat {
			encoderConfig := zapConfig.EncoderConfig
			encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
			encoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder
			encoderConfig.EncodeCaller = zapcore.ShortCallerEncoder
			
			Logger = zap.New(
				zapcore.NewCore(
					NewScalyrEncoder(encoderConfig),
					zapcore.AddSync(os.Stdout),
					zapcore.Level(level),
				),
				zap.AddCaller(),
				zap.AddStacktrace(zapcore.ErrorLevel),
			)
			return nil
		}
	}

	// Standard encoder
	var err error
	Logger, err = zapConfig.Build(
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
	)
	if err != nil {
		return err
	}

	return nil
}

// GetLogger returns the global logger
func GetLogger() *zap.Logger {
	if Logger == nil {
		// Fallback to default logger
		Logger, _ = zap.NewProduction()
	}
	return Logger
}

// WithContext adds context fields to logger
func WithContext(fields ...zap.Field) *zap.Logger {
	return GetLogger().With(fields...)
}

// WithTraceID adds trace ID to logger
func WithTraceID(traceID string) *zap.Logger {
	return GetLogger().With(zap.String("trace_id", traceID))
}

// WithSpanID adds span ID to logger
func WithSpanID(spanID string) *zap.Logger {
	return GetLogger().With(zap.String("span_id", spanID))
}

// WithService adds service name to logger
func WithService(service string) *zap.Logger {
	return GetLogger().With(zap.String("service", service))
}

// WithComponent adds component name to logger
func WithComponent(component string) *zap.Logger {
	return GetLogger().With(zap.String("component", component))
}

