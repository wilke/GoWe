package toolexec

// tailBuffer is an io.Writer that retains only roughly the last `limit` bytes
// written, bounding memory even when a tool streams unbounded output to
// stdout/stderr (GoWe#134). The earlier `bytes.Buffer` + post-hoc tail slice
// buffered the entire stream first, which could OOM the worker on tools that
// emit multi-GB logs. tailBuffer caps the in-memory copy during capture and
// String() returns exactly the tail bytes/truncation-marker that the old
// `bytes.Buffer` + tailString(buf, maxLogCapture) pair produced, so callers
// see identical output for both under-cap and over-cap streams.
//
// Compaction is amortized: the backing slice is allowed to grow to 2*limit
// before the oldest bytes are dropped, so steady-state writes don't
// reallocate/copy on every call. The slice never grows past 2*limit, so
// memory use is O(limit) regardless of total bytes written.
//
// Concurrency: tailBuffer is not internally synchronized and Write must not
// be called concurrently on the same instance. This is safe in practice
// because every call site constructs one tailBuffer per stream (stdout and
// stderr each get their own instance, never shared) and feeds it either
// directly as cmd.Stdout/cmd.Stderr or wrapped in io.MultiWriter(file, buf).
// os/exec copies each of a command's stdout and stderr through exactly one
// goroutine per stream (see os/exec's stdout/stderr pipe-copy goroutines),
// and io.MultiWriter itself calls the underlying writers' Write sequentially
// from whichever single goroutine invoked it — so a given tailBuffer instance
// only ever sees writes from one goroutine at a time. If a future call site
// ever shares one tailBuffer between multiple concurrently-written streams,
// it must add its own locking; this type intentionally stays lock-free to
// keep the hot path (every stdout/stderr chunk of every task) allocation- and
// contention-free.
type tailBuffer struct {
	limit     int
	buf       []byte
	truncated bool
}

// newTailBuffer creates a tailBuffer that retains at most `limit` bytes.
func newTailBuffer(limit int) *tailBuffer { return &tailBuffer{limit: limit} }

// Write appends p, compacting the backing slice back down to `limit` bytes
// once it has grown past 2*limit. Always returns (len(p), nil): tailBuffer
// never fails a write, mirroring bytes.Buffer's behavior as an io.Writer.
func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.truncated = true
		if len(t.buf) > 2*t.limit {
			t.buf = append([]byte(nil), t.buf[len(t.buf)-t.limit:]...)
		}
	}
	return len(p), nil
}

// String returns the retained tail, prefixed with a truncation marker when
// earlier output was dropped.
func (t *tailBuffer) String() string {
	b := t.buf
	if len(b) > t.limit {
		b = b[len(b)-t.limit:]
	}
	if t.truncated {
		return "... [truncated] ...\n" + string(b)
	}
	return string(b)
}
