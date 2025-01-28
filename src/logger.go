package src

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func createLogger(verbose bool) *zap.Logger {
	cfg := zap.Config{
		Encoding:    "console",
		OutputPaths: []string{"stderr"},
		EncoderConfig: zapcore.EncoderConfig{
			MessageKey:       "message",
			LevelKey:         "level",
			EncodeLevel:      zapcore.CapitalLevelEncoder,
			ConsoleSeparator: "  ",
		},
		Level: zap.NewAtomicLevelAt(zap.DebugLevel),
	}

	if !verbose {
		cfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	logger, err := cfg.Build()
	if err != nil {
		panic(err)
	}

	return logger
}
