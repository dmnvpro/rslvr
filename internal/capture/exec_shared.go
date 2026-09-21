package capture

import "bytes"

// limitedBuffer collects at most limit bytes; the overflow is dropped so a
// runaway encoder cannot exhaust memory (frame cap, TechSpec §7).
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.limit - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	// Always accept the full write: the child must not see write errors.
	return len(p), nil
}

func (l *limitedBuffer) data() []byte { return l.buf.Bytes() }
