package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/macho"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDisposableUpdateHelper(t *testing.T) {
	address := os.Getenv("LU_UPDATE_TEST_SERVER")
	if address == "" {
		t.Skip("subprocess fixture")
	}
	destination, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Version: "0.4.1", StateDir: os.Getenv("LU_UPDATE_TEST_STATE")})
	s.client.Transport = localTransport{base: http.DefaultTransport, destination: destination}
	p := &testPrompt{yes: true}
	if err := s.Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	fmt.Println(strings.Join(p.notices, "\n"))
	fmt.Println("current invocation 0.4.1")
}

func TestDisposableExecutableUpdatesOnNextInvocation(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two disposable native release slices")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte("package main\nfunc main(){println(\"lu-cleaner 0.4.2\")}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var slices [][]byte
	for _, arch := range []string{"amd64", "arm64"} {
		path := filepath.Join(dir, arch)
		cmd := exec.Command("go", "build", "-p", "2", "-o", path, source)
		cmd.Env = append(os.Environ(), "GOOS=darwin", "GOARCH="+arch, "CGO_ENABLED=0", "GOMAXPROCS=2")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", arch, err, output)
		}
		slices = append(slices, readFile(t, path))
	}
	offsets := []int{16384, (16384 + len(slices[0]) + 16383) &^ 16383}
	fat := make([]byte, offsets[1]+len(slices[1]))
	binary.BigEndian.PutUint32(fat, 0xcafebabe)
	binary.BigEndian.PutUint32(fat[4:], 2)
	for i, cpu := range []macho.Cpu{macho.CpuAmd64, macho.CpuArm64} {
		sub := uint32(0)
		if i == 0 {
			sub = 3
		}
		for j, n := range []uint32{uint32(cpu), sub, uint32(offsets[i]), uint32(len(slices[i])), 14} {
			binary.BigEndian.PutUint32(fat[8+i*20+j*4:], n)
		}
		copy(fat[offsets[i]:], slices[i])
	}
	archive := makeArchive(t, tarEntry{name: "lu-cleaner", data: fat})
	var calls atomic.Int32
	s, target := testService(t, fixtureHandler(archive, &calls))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	old := readFile(t, executable)
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	tr := s.client.Transport.(localTransport)
	cmd := exec.Command(target, "-test.run=^TestDisposableUpdateHelper$")
	cmd.Env = append(os.Environ(), "LU_UPDATE_TEST_SERVER="+tr.destination.String(), "LU_UPDATE_TEST_STATE="+s.options.StateDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("old invocation: %v %s", err, output)
	}
	if !bytes.Contains(output, []byte("current invocation 0.4.1")) || !bytes.Contains(output, []byte("next invocation uses 0.4.2")) {
		t.Fatalf("old invocation lifecycle: %s", output)
	}
	output, err = exec.Command(target).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "lu-cleaner 0.4.2" {
		t.Fatalf("next invocation: %v %s", err, output)
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".lu-cleaner-backup-*"))
	if len(backups) != 1 || sha256.Sum256(readFile(t, backups[0])) != sha256.Sum256(old) {
		t.Fatal("backup differs from executed old binary")
	}
	if calls.Load() != 3 {
		t.Fatalf("network requests %d", calls.Load())
	}
}
