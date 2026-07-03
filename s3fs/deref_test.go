package s3fs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestDerefHelpers covers the nil-safe dereference helpers that guard the
// AWS-SDK pointer fields (ContentLength, LastModified, Size). S3-compatible
// servers may leave these nil, which previously caused nil-pointer panics in
// Stat and OpenReader.
func TestDerefHelpers(t *testing.T) {
	t.Run("derefInt64 nil", func(t *testing.T) {
		assert.Equal(t, int64(0), derefInt64(nil))
	})
	t.Run("derefInt64 value", func(t *testing.T) {
		v := int64(1234)
		assert.Equal(t, int64(1234), derefInt64(&v))
	})

	t.Run("derefTime nil", func(t *testing.T) {
		assert.True(t, derefTime(nil).IsZero())
	})
	t.Run("derefTime value", func(t *testing.T) {
		ts := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
		assert.Equal(t, ts, derefTime(&ts))
	})
}
