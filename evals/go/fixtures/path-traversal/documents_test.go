package documents

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadDocumentRejectsEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "documents")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDocument(root, "../secret.txt"); err == nil {
		t.Fatal("ReadDocument accepted path traversal")
	}
}

func TestReadDocumentReadsChild(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "public.txt"), []byte("public"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadDocument(root, "public.txt")
	if err != nil || string(data) != "public" {
		t.Fatalf("ReadDocument() = %q, %v", data, err)
	}
}
