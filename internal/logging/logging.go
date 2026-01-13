package logging

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

type Logger struct {
	info  *log.Logger
	error *log.Logger
}

func NewLogger(cacheDir string) (*Logger, error) {
	if cacheDir == "" {
		return nil, fmt.Errorf("cache directory required")
	}
	logDir := filepath.Join(cacheDir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	logFile := filepath.Join(logDir, "smartpasta-daemon.log")
	file, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	flags := log.LstdFlags | log.LUTC
	return &Logger{
		info:  log.New(file, "INFO ", flags),
		error: log.New(file, "ERROR ", flags),
	}, nil
}

func (l *Logger) Infof(format string, args ...any) {
	if l == nil || l.info == nil {
		return
	}
	l.info.Printf(format, args...)
}

func (l *Logger) Errorf(format string, args ...any) {
	if l == nil || l.error == nil {
		return
	}
	l.error.Printf(format, args...)
}

func TimestampedFilename(prefix string, ext string, t time.Time) string {
	return fmt.Sprintf("%s-%s%s", prefix, t.Format("2006-01-02 15:04:05"), ext)
}
