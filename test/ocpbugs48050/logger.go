package ocpbugs48050

import "log"

type logger interface {
	Logf(format string, args ...interface{})
}

type stdoutLogger struct{}

func (l *stdoutLogger) Logf(format string, args ...interface{}) {
	log.Printf(format, args...)
}

type devNullLogger struct{}

func (l *devNullLogger) Logf(format string, args ...interface{}) {
	// Do nothing, effectively discards logs.
}
