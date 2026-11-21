package telemetry

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func queueTestDir(t *testing.T) string {
	t.Helper()
	// System temporary directories can have a symlink prefix (e.g. macOS /var).
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOpenQueueFile_RejectsSymlinks(t *testing.T) {
	for _, component := range []string{"file", "parent", "ancestor"} {
		t.Run(component, func(t *testing.T) {
			dir := queueTestDir(t)
			outside := queueTestDir(t)
			path := filepath.Join(dir, "queue.jsonl")
			sentinel := filepath.Join(outside, "queue.jsonl")
			data := bytes.Repeat([]byte("sentinel\n"), 150000)
			if err := os.WriteFile(sentinel, data, 0600); err != nil {
				t.Fatal(err)
			}
			link, target := path, sentinel
			if component != "file" {
				link, target = filepath.Join(dir, "link"), outside
				path = filepath.Join(link, "queue.jsonl")
				if component == "ancestor" {
					path = filepath.Join(link, "new", "queue.jsonl")
				}
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			for _, flag := range []int{os.O_WRONLY | os.O_APPEND | os.O_CREATE, os.O_RDONLY, os.O_WRONLY} {
				if f, err := openQueueFile(path, flag); err == nil {
					f.Close()
					t.Fatal("opened a symlinked queue")
				}
			}
			got, err := os.ReadFile(sentinel)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("external sentinel changed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
				t.Fatal("created a directory through an ancestor symlink")
			}
		})
	}
}

func TestOpenQueueFile_PinsValidatedHandle(t *testing.T) {
	for _, component := range []string{"file", "parent"} {
		t.Run(component, func(t *testing.T) {
			dir := filepath.Join(queueTestDir(t), "private")
			path := filepath.Join(dir, "queue.jsonl")
			f, err := openQueueFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			outside := queueTestDir(t)
			sentinel := filepath.Join(outside, "queue.jsonl")
			data := []byte("leave this file alone")
			if err := os.WriteFile(sentinel, data, 0600); err != nil {
				t.Fatal(err)
			}
			link, target := path, sentinel
			if component == "parent" {
				link, target = dir, outside
			}
			if err := os.Rename(link, link+".saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if err := f.Truncate(0); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte("event\n")); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(sentinel)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("handle followed substituted symlink: %v", err)
			}
		})
	}
}

func TestOpenQueueFile_RejectsNonRegularFile(t *testing.T) {
	path := filepath.Join(queueTestDir(t), "queue.jsonl")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []int{os.O_WRONLY | os.O_APPEND | os.O_CREATE, os.O_RDONLY, os.O_WRONLY} {
		if f, err := openQueueFile(path, flag); err == nil {
			f.Close()
			t.Fatal("opened a non-regular queue")
		}
	}
}
