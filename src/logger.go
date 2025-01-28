package src

import "go.uber.org/zap"

func createLogger(verbose bool) *zap.Logger {
	cfg := zap.NewDevelopmentConfig()
	if !verbose {
		cfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	logger, err := cfg.Build()
	if err != nil {
		panic(err)
	}

	return logger
}
