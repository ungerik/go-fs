package azureblobfs

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRedactErr checks the redaction of URL queries and SAS signatures.
func TestRedactErr(t *testing.T) {
	assert.NoError(t, redactErr(nil))
	plain := errors.New("no URL here")
	assert.Same(t, plain, redactErr(plain), "errors without secrets are returned unchanged")

	for msg, want := range map[string]string{
		`Get "https://a.blob.core.windows.net/c/x.txt?sv=1&sig=abc%2F": dial tcp: refused`: `Get "https://a.blob.core.windows.net/c/x.txt": dial tcp: refused`,
		`PUT http://127.0.0.1:1/acc/c?comp=list&sig=abc and more`:                          `PUT http://127.0.0.1:1/acc/c and more`,
		`signature sig=abc&se=2026 rejected`:                                               `signature sig=REDACTED&se=2026 rejected`,
		`two https://h/a?x=1 and https://h/b?sig=2#frag`:                                   `two https://h/a and https://h/b#frag`,
	} {
		err := redactErr(fmt.Errorf("wrapped: %w", errors.New(msg)))
		assert.Equal(t, "wrapped: "+want, err.Error(), "redactErr(%q)", msg)
	}
	err := redactErr(fmt.Errorf("x?sig=1: %w", context.Canceled))
	assert.ErrorIs(t, err, context.Canceled, "redacted errors unwrap")
}
