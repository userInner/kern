package extract

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func writeArchive(t *testing.T, name, entry string) {
	t.Helper()
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	writer, err := archive.Create(entry)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write([]byte("payload"))
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestZIPRejectsTraversal(t *testing.T) {
	parent := t.TempDir()
	archive := filepath.Join(parent, "payload.zip")
	destination := filepath.Join(parent, "output")
	writeArchive(t, archive, "../escaped.txt")
	if err := ZIP(archive, destination); err == nil {
		t.Fatal("ZIP accepted traversal entry")
	}
	if _, err := os.Stat(filepath.Join(parent, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatalf("escaped file exists: %v", err)
	}
}

func TestZIPExtractsNestedFile(t *testing.T) {
	parent := t.TempDir()
	archive := filepath.Join(parent, "payload.zip")
	destination := filepath.Join(parent, "output")
	writeArchive(t, archive, "nested/file.txt")
	if err := ZIP(archive, destination); err != nil {
		t.Fatalf("ZIP() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "nested", "file.txt"))
	if err != nil || string(data) != "payload" {
		t.Fatalf("extracted data = %q, %v", data, err)
	}
}
