// Package logger provides custom log levels mirroring the Python source:
// TRACE < HINT < DEBUG < INFO < NOTE < WARNING < DONE < ERROR < FATAL
package logger

import (
	"fmt"
	"log"
	"os"
)

// Numeric level constants – intentionally match the Python values.
const (
	TRACE   = 5  // (DEBUG+NOTSET)/2
	HINT    = 7  // (DEBUG+INFO)/2  – was 7 in Python with NOTSET=0,DEBUG=10
	DEBUG   = 10
	INFO    = 20
	NOTE    = 25 // (WARNING+INFO)/2
	WARNING = 30
	DONE    = 35 // (WARNING+ERROR)/2
	ERROR   = 40
	FATAL   = 50
)

// Logger is a levelled logger compatible with the systemctl codebase.
type Logger struct {
	level  int
	logger *log.Logger
	sinks  []*sink
}

var std = &Logger{
	level:  NOTE,
	logger: log.New(os.Stderr, "", log.Ldate|log.Ltime),
}

// GetLogger returns the package-level logger.
func GetLogger(name string) *Logger {
	return std
}

// SetLevel changes the minimum log level.
func (l *Logger) SetLevel(level int) {
	l.level = level
}

// Level returns the current minimum level.
func (l *Logger) Level() int {
	return l.level
}

// AddFileHandler adds a file as an additional log output at the given minimum level.
func (l *Logger) AddFileHandler(path string, level int) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	l.sinks = append(l.sinks, &sink{logger: log.New(f, "", log.Ldate|log.Ltime), level: level})
	return nil
}

type sink struct {
	logger *log.Logger
	level  int
}

// sinks holds extra file-based outputs.
func init() { std.sinks = []*sink{} }

func (l *Logger) log(level int, format string, args ...any) {
	if level < l.level {
		return
	}
	prefix := levelName(level)
	msg := fmt.Sprintf(format, args...)
	l.logger.Printf("%s %s", prefix, msg)
	for _, s := range l.sinks {
		if level >= s.level {
			s.logger.Printf("%s %s", prefix, msg)
		}
	}
}

func levelName(level int) string {
	switch {
	case level <= TRACE:
		return "TRACE"
	case level <= HINT:
		return "HINT"
	case level <= DEBUG:
		return "DEBUG"
	case level <= INFO:
		return "INFO"
	case level <= NOTE:
		return "NOTE"
	case level <= WARNING:
		return "WARNING"
	case level <= DONE:
		return "DONE"
	case level <= ERROR:
		return "ERROR"
	default:
		return "FATAL"
	}
}

// Convenience methods on Logger.
func (l *Logger) Tracef(format string, args ...any) { l.log(TRACE, format, args...) }
func (l *Logger) Hintf(format string, args ...any)  { l.log(HINT, format, args...) }
func (l *Logger) Debugf(format string, args ...any) { l.log(DEBUG, format, args...) }
func (l *Logger) Infof(format string, args ...any)  { l.log(INFO, format, args...) }
func (l *Logger) Notef(format string, args ...any)  { l.log(NOTE, format, args...) }
func (l *Logger) Warnf(format string, args ...any)  { l.log(WARNING, format, args...) }
func (l *Logger) Donef(format string, args ...any)  { l.log(DONE, format, args...) }
func (l *Logger) Errorf(format string, args ...any) { l.log(ERROR, format, args...) }
func (l *Logger) Fatalf(format string, args ...any) { l.log(FATAL, format, args...); os.Exit(1) }

// Log emits at an arbitrary numeric level (mirrors Python's logg.log(LEVEL, …)).
func (l *Logger) Log(level int, format string, args ...any) { l.log(level, format, args...) }
