package toolexec

import (
	"bytes"
	"strings"
	"testing"
)

// TestTailBuffer_SingleWrite covers the writer's single-write behavior: under
// cap the content is intact and unmarked; over cap it retains exactly the
// last `limit` bytes with the truncation marker prepended, matching the
// pre-fix bytes.Buffer + tailString(buf, maxLogCapture) output byte-for-byte.
func TestTailBuffer_SingleWrite(t *testing.T) {
	tests := []struct {
		name  string
		input string
		limit int
		want  string
	}{
		{
			name:  "empty write",
			input: "",
			limit: 100,
			want:  "",
		},
		{
			name:  "under cap returns full content intact",
			input: "hello world",
			limit: 100,
			want:  "hello world",
		},
		{
			name:  "exactly at cap returns full content, no marker",
			input: "12345",
			limit: 5,
			want:  "12345",
		},
		{
			name:  "over cap truncates to exact last-N bytes with marker",
			input: "abcdefghij",
			limit: 5,
			want:  "... [truncated] ...\nfghij",
		},
		{
			name:  "single write far larger than cap keeps only the tail",
			input: strings.Repeat("x", 1000) + "TAIL",
			limit: 10,
			want:  "... [truncated] ...\nxxxxxxTAIL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := newTailBuffer(tt.limit)
			n, err := buf.Write([]byte(tt.input))
			if err != nil {
				t.Fatalf("Write returned error: %v", err)
			}
			if n != len(tt.input) {
				t.Errorf("Write returned n=%d, want %d (io.Writer contract: report all bytes accepted)", n, len(tt.input))
			}
			got := buf.String()
			if got != tt.want {
				t.Errorf("tailBuffer(%d bytes, limit=%d) = %q, want %q",
					len(tt.input), tt.limit, got, tt.want)
			}
		})
	}
}

// TestTailBuffer_AllocationBounded asserts the writer's fixed-allocation
// property directly against its own internal backing slice (cap, not
// runtime memstats): after streaming far more than `limit` bytes, the
// backing array must never have grown past 2*limit, regardless of how much
// total data was written or how it was chunked.
func TestTailBuffer_AllocationBounded(t *testing.T) {
	tests := []struct {
		name       string
		limit      int
		writeSize  int // bytes per Write call
		writeCount int
	}{
		{name: "many small writes", limit: 1024, writeSize: 16, writeCount: 10_000},
		{name: "many medium writes", limit: 4096, writeSize: 4096, writeCount: 256},
		{name: "few huge writes, each larger than the cap", limit: 1024, writeSize: 64 * 1024, writeCount: 8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tb := newTailBuffer(tt.limit)
			chunk := bytes.Repeat([]byte{'a'}, tt.writeSize)
			for i := 0; i < tt.writeCount; i++ {
				if _, err := tb.Write(chunk); err != nil {
					t.Fatalf("Write: %v", err)
				}
			}
			totalWritten := tt.writeSize * tt.writeCount
			if len(tb.buf) > 2*tb.limit {
				t.Errorf("backing buffer len=%d exceeds 2*limit=%d after writing %d bytes total (unbounded growth)",
					len(tb.buf), 2*tb.limit, totalWritten)
			}
			if cap(tb.buf) > 4*tb.limit {
				// Generous headroom for append's growth strategy; the point is
				// cap must stay a small constant multiple of limit, never grow
				// proportional to totalWritten (which can be arbitrarily large).
				t.Errorf("backing buffer cap=%d is not bounded relative to limit=%d after writing %d bytes total",
					cap(tb.buf), tb.limit, totalWritten)
			}
		})
	}
}

// TestTailBuffer_WraparoundCorrectness verifies the tail is byte-for-byte
// correct across many writes that individually straddle the compaction
// threshold (2*limit), including writes larger than the cap itself, by
// comparing against a naive "keep everything, slice the tail at the end"
// reference implementation.
func TestTailBuffer_WraparoundCorrectness(t *testing.T) {
	const limit = 100

	type write struct {
		size byte // content byte to write, and (via index) a distinguishable length
		n    int
	}
	writes := []write{
		{size: 'a', n: 30},  // under limit
		{size: 'b', n: 90},  // pushes total over limit, still under 2*limit
		{size: 'c', n: 50},  // crosses 2*limit, triggers compaction
		{size: 'd', n: 5},   // small write after compaction
		{size: 'e', n: 250}, // single write itself larger than the cap
		{size: 'f', n: 100}, // exactly at the cap
		{size: 'g', n: 1},   // final single byte, must survive as the very last byte
	}

	tb := newTailBuffer(limit)
	var reference []byte
	for _, w := range writes {
		chunk := bytes.Repeat([]byte{w.size}, w.n)
		if _, err := tb.Write(chunk); err != nil {
			t.Fatalf("Write: %v", err)
		}
		reference = append(reference, chunk...)

		// After every write, the backing buffer must stay bounded...
		if len(tb.buf) > 2*limit {
			t.Fatalf("after writing %d bytes of %q: backing buffer len=%d exceeds 2*limit=%d",
				w.n, string(w.size), len(tb.buf), 2*limit)
		}
	}

	wantTail := reference
	truncated := len(reference) > limit
	if truncated {
		wantTail = reference[len(reference)-limit:]
	}
	want := string(wantTail)
	if truncated {
		want = "... [truncated] ...\n" + want
	}

	got := tb.String()
	if got != want {
		t.Errorf("wraparound tail mismatch:\n got=%q\nwant=%q", got, want)
	}
	if !strings.HasSuffix(got, "g") {
		t.Errorf("final byte lost: got suffix %q", tailSuffix(got, 5))
	}
}

// TestTailBuffer_ChunkedStreamPreservesTail verifies the tail is retained
// across many small writes (simulating a streaming tool) and that the
// truncation marker appears once output exceeds the cap.
func TestTailBuffer_ChunkedStreamPreservesTail(t *testing.T) {
	tb := newTailBuffer(1024)
	chunk := []byte(strings.Repeat("a", 4096))
	for i := 0; i < 256; i++ {
		_, _ = tb.Write(chunk)
	}
	_, _ = tb.Write([]byte("ENDSENTINEL"))

	if len(tb.buf) > 2*tb.limit {
		t.Errorf("backing buffer grew to %d bytes, want <= %d (unbounded growth)", len(tb.buf), 2*tb.limit)
	}
	got := tb.String()
	if !strings.HasSuffix(got, "ENDSENTINEL") {
		t.Errorf("tail lost the final content: got suffix %q", tailSuffix(got, 20))
	}
	if !strings.HasPrefix(got, "... [truncated] ...\n") {
		t.Errorf("expected truncation marker, got %q", got[:30])
	}
}
