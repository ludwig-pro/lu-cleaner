package fsx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAppDataProtected(t *testing.T) {
	home, _ := os.UserHomeDir()
	if FullDiskAccess() {
		t.Skip("process has Full Disk Access: nothing is protected")
	}
	cases := map[string]bool{
		filepath.Join(home, "Library/Containers"):                             false, // the root may be listed
		filepath.Join(home, "Library/Containers/com.apple.Foo/Data"):          true,
		filepath.Join(home, "library/group containers/ABC.group/Library"):     true, // case-insensitive
		filepath.Join(home, "Library/Caches/com.apple.Foo"):                   false,
		filepath.Join(home, "Library/Containers/com.docker.docker/Data/log"):  true,
		filepath.Join(home, "Library/Group Containers/2BBY89MBSN.dev.warp/x"): true,
		filepath.Join(home, "Library/ContainersExtra/whatever"):               false,
	}
	for p, want := range cases {
		if got := AppDataProtected(p); got != want {
			t.Errorf("AppDataProtected(%s) = %v, want %v", p, got, want)
		}
	}
	if !GlobPrefixProtected(filepath.Join(home, "Library/Containers/*/Data/Library/Caches")) {
		t.Errorf("glob across containers must be protected")
	}
	_, err := Size(context.Background(), filepath.Join(home, "Library/Containers/com.apple.AMPArtworkAgent/Data"), nil)
	if !errors.Is(err, ErrNeedsFullDiskAccess) {
		t.Errorf("Size inside a container: err = %v, want ErrNeedsFullDiskAccess", err)
	}
}
