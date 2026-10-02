package scanctl

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
)

func TestPolicyLifecycleInSubprocess(t *testing.T) {
	if kind := os.Getenv("LU_SCAN_POLICY_TEST"); kind != "" {
		runtime.GOMAXPROCS(3)
		before, _ := currentBackground()
		if kind == "inherited" {
			if err := setBackground(true); err != nil {
				t.Fatal(err)
			}
		}
		inherited, _ := currentBackground()
		if kind == "failure" {
			applyBackground = func() (func() error, error) { return func() error { return nil }, errors.New("fixture denied") }
		}
		l, _, _ := Resolve("eco", "")
		restore, err := Activate(l)
		if kind == "failure" && err == nil {
			t.Fatal("missing activation error")
		}
		if kind != "failure" && err != nil {
			t.Fatal(err)
		}
		if runtime.GOMAXPROCS(0) != 2 {
			t.Fatal("eco runtime limit not applied")
		}
		if kind != "failure" {
			if bg, e := currentBackground(); e != nil || !bg {
				t.Fatalf("not background: %v %v", bg, e)
			}
		}
		if err := restore(); err != nil {
			t.Fatal(err)
		}
		if err := restore(); err != nil {
			t.Fatal(err)
		}
		if runtime.GOMAXPROCS(0) != 3 {
			t.Fatal("runtime limit not restored")
		}
		if bg, e := currentBackground(); e != nil || bg != inherited {
			t.Fatalf("inherited policy changed: %v %v", bg, e)
		}
		if kind == "inherited" && !before {
			if err := setBackground(false); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	for _, kind := range []string{"normal", "inherited", "failure"} {
		t.Run(kind, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestPolicyLifecycleInSubprocess$")
			cmd.Env = append(os.Environ(), "LU_SCAN_POLICY_TEST="+kind)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("subprocess %v: %s", err, out)
			}
		})
	}
}
