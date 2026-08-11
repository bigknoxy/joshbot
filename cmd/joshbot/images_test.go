package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePNG(t *testing.T, path string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{G: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return buf.Bytes()
}

// TestImageFlagLoadsRepeatably — --image is documented as repeatable, and a
// flag that silently kept only the last path would answer about the wrong
// picture with no error anywhere.
func TestImageFlagLoadsRepeatably(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.png")
	b := filepath.Join(dir, "b.png")
	wantA := writePNG(t, a)
	writePNG(t, b)

	images, err := loadImageFlags([]string{a, b})
	if err != nil {
		t.Fatalf("loadImageFlags: %v", err)
	}
	if len(images) != 2 {
		t.Fatalf("got %d images, want 2", len(images))
	}
	if !bytes.Equal(images[0].Data, wantA) {
		t.Fatal("the first image is not the first path's bytes")
	}
	if images[0].MIME != "image/png" {
		t.Fatalf("MIME = %q, want image/png", images[0].MIME)
	}

	if got, err := loadImageFlags(nil); err != nil || got != nil {
		t.Fatalf("no --image must be no images and no error, got %v %v", got, err)
	}
}

// TestBadImagePathFailsNamingThePath — the whole point of loading up front is
// a legible failure. An error that does not say which of several paths was bad
// leaves the user guessing.
func TestBadImagePathFailsNamingThePath(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.png")
	writePNG(t, good)
	missing := filepath.Join(dir, "nope.png")

	_, err := loadImageFlags([]string{good, missing})
	if err == nil {
		t.Fatal("a nonexistent path must fail the invocation")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("the error must name the offending path, got %v", err)
	}

	// A directory reads as a path that exists, which is exactly why it needs
	// its own check rather than relying on ReadFile's error.
	if _, err := loadImageFlags([]string{dir}); err == nil {
		t.Fatal("a directory must be rejected")
	}

	// The extension is not evidence: a text file named .png must be refused
	// here, the same as on any channel.
	prose := filepath.Join(dir, "notes.png")
	if err := os.WriteFile(prose, []byte("this is prose, not an image\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	err = loadImageFlagsErr(t, prose)
	if !strings.Contains(err.Error(), prose) {
		t.Fatalf("the error must name the path, got %v", err)
	}
}

func loadImageFlagsErr(t *testing.T, path string) error {
	t.Helper()
	_, err := loadImageFlags([]string{path})
	if err == nil {
		t.Fatalf("%s must be rejected", path)
	}
	return err
}
