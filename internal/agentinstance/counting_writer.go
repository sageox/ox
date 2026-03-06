package agentinstance

import "io"

// CountingWriter wraps an io.Writer and counts bytes written through it.
// Used to measure how much context ox commands inject into agent windows.
type CountingWriter struct {
	inner   io.Writer
	written int64
}

// NewCountingWriter wraps w with byte counting.
func NewCountingWriter(w io.Writer) *CountingWriter {
	return &CountingWriter{inner: w}
}

// Write passes through to the inner writer and counts bytes.
func (cw *CountingWriter) Write(p []byte) (int, error) {
	n, err := cw.inner.Write(p)
	cw.written += int64(n)
	return n, err
}

// BytesWritten returns the total bytes written through this writer.
func (cw *CountingWriter) BytesWritten() int64 {
	return cw.written
}
