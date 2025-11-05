package fs

import (
	"fmt"
	"io"
)

// Logger is an interface for logging formatted messages.
// It provides a Printf method similar to fmt.Printf but allows
// different implementations for various logging backends.
//
// Logger can be used for error logging, debugging output,
// or any formatted message output within the fs package.
type Logger interface {
	// Printf formats and logs a message according to a format specifier.
	// It follows the same formatting rules as fmt.Printf.
	Printf(format string, args ...any)
}

// LoggerFunc is a function type that implements the Logger interface.
// It allows any function with the signature func(string, ...any) to be used as a Logger.
// This is useful for creating inline logger implementations or wrapping existing
// logging functions without defining a new type.
//
// Example:
//
//	logger := LoggerFunc(func(format string, args ...any) {
//		log.Printf("[FS] " + format, args...)
//	})
//	logger.Printf("operation completed: %s", filename)
type LoggerFunc func(format string, args ...any)

// Printf calls the underlying function, satisfying the Logger interface.
func (f LoggerFunc) Printf(format string, args ...any) {
	f(format, args...)
}

// WriterLogger returns a Logger that writes formatted output to the given io.Writer.
// The logger uses fmt.Fprintf internally to format and write the log messages.
// Any errors from the write operation are silently ignored.
//
// This is useful for directing log output to files, buffers, network connections,
// or any other io.Writer implementation. Common use cases include logging to
// os.Stderr, os.Stdout, or a log file.
//
// Example:
//
//	// Log to stderr
//	logger := WriterLogger(os.Stderr)
//	logger.Printf("error processing file: %s", err)
//
//	// Log to a file
//	f, _ := os.Create("app.log")
//	defer f.Close()
//	logger := WriterLogger(f)
//	logger.Printf("started at %s", time.Now())
func WriterLogger(w io.Writer) Logger {
	return LoggerFunc(func(format string, args ...any) {
		_, _ = fmt.Fprintf(w, format, args...)
	})
}
