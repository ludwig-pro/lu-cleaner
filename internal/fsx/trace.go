package fsx

import (
	"fmt"
	"os"
	"time"
)

// Tracing for performance work: LU_TRACE=1 prints every directory walk and
// external command slower than LU_TRACE_MS (default 300 ms) to stderr.
var (
	traceOn  = os.Getenv("LU_TRACE") != ""
	traceMin = func() time.Duration {
		var ms int
		if _, err := fmt.Sscan(os.Getenv("LU_TRACE_MS"), &ms); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
		return 300 * time.Millisecond
	}()
)

// Trace reports a slow operation when LU_TRACE is set. kind is "walk",
// "cmd"…; start is when the operation began.
func Trace(kind, what string, start time.Time, detail string) {
	if !traceOn {
		return
	}
	if d := time.Since(start); d >= traceMin {
		fmt.Fprintf(os.Stderr, "[trace] %-5s %7.2fs  %s  %s\n", kind, d.Seconds(), what, detail)
	}
}
