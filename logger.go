package fs

// Logger is an interface that can be implemented to log errors
type Logger interface {
	Printf(format string, args ...any)
}
