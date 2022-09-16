package fs

// Logger is an interface that can be implemented to log errors
type Logger interface {
	Printf(format string, args ...any)
}

// LoggerFunc implements Logger as higher order function
type LoggerFunc func(format string, args ...any)

func (f LoggerFunc) Printf(format string, args ...any) {
	f(format, args...)
}
