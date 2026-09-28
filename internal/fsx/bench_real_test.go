package fsx

import (
	"context"
	"os"
	"testing"
	"time"
)

// LU_BENCH_PATH=/some/dir go test -run TestRealWalk -v ./internal/fsx
func TestRealWalk(t *testing.T) {
	p := os.Getenv("LU_BENCH_PATH")
	if p == "" {
		t.Skip("set LU_BENCH_PATH")
	}
	start := time.Now()
	st, err := Size(context.Background(), p, nil)
	t.Logf("%s: %s, %d files, %d dirs, reclaim %s, errors %d, in %v (err %v)", p, Bytes(st.Bytes), st.Files, st.Dirs, Bytes(st.Reclaim), st.Errors, time.Since(start), err)
}
