package bfcp

// Logger interface for structured logging, compatible with livekit protocol logger.
type Logger interface {
	Debugw(msg string, keysAndValues ...interface{})
	Infow(msg string, keysAndValues ...interface{})
	Warnw(msg string, err error, keysAndValues ...interface{})
	Errorw(msg string, err error, keysAndValues ...interface{})
}

// NopLogger discards all log output.
type NopLogger struct{}

func (NopLogger) Debugw(string, ...interface{})       {}
func (NopLogger) Infow(string, ...interface{})        {}
func (NopLogger) Warnw(string, error, ...interface{}) {}
func (NopLogger) Errorw(string, error, ...interface{}) {}
