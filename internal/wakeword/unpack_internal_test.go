package wakeword

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnpackWritesTheEmbeddedProject(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // Linux
	t.Setenv("HOME", t.TempDir())           // macOS derives the cache from HOME

	dir, err := unpack()
	if err != nil {
		t.Fatal(err)
	}

	// uv needs all three files: the script, its project, and the lock that pins the device install.
	for _, name := range []string{"wakeword.py", "pyproject.toml", "uv.lock"} {
		want, embedErr := project.ReadFile(name)
		if embedErr != nil {
			t.Fatalf("%s is not embedded: %v", name, embedErr)
		}
		got, readErr := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // Reads the file the test unpacked.
		if readErr != nil || string(got) != string(want) {
			t.Fatalf("unpacked %s differs from the embedded copy: %v", name, readErr)
		}
	}
}
