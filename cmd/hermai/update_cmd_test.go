package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAssetNameFor(t *testing.T) {
	name, ext := assetNameFor("0.5.0")

	wantExt := ".tar.gz"
	if runtime.GOOS == "windows" {
		wantExt = ".zip"
	}
	if ext != wantExt {
		t.Fatalf("ext = %q, want %q", ext, wantExt)
	}
	wantName := "hermai_0.5.0_" + runtime.GOOS + "_" + runtime.GOARCH + wantExt
	if name != wantName {
		t.Fatalf("name = %q, want %q", name, wantName)
	}
}

func TestFindAsset(t *testing.T) {
	assets := []releaseAsset{
		{Name: "hermai_0.5.0_linux_amd64.tar.gz"},
		{Name: "hermai_0.5.0_darwin_arm64.tar.gz"},
		{Name: "checksums.txt"},
	}
	got := findAsset(assets, "checksums.txt")
	if got == nil || got.Name != "checksums.txt" {
		t.Fatalf("expected to find checksums.txt, got %+v", got)
	}
	if findAsset(assets, "missing.tar.gz") != nil {
		t.Fatalf("expected nil for missing asset")
	}
}

func TestVerifySHA256(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "payload")
	payload := []byte("hello hermai")
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	good := hex.EncodeToString(sum[:])

	if err := verifySHA256(tmp, good); err != nil {
		t.Fatalf("expected match, got %v", err)
	}
	if err := verifySHA256(tmp, strings.Repeat("0", 64)); err == nil {
		t.Fatalf("expected mismatch error")
	}
}

func TestExtractBinary_TarGz(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tar.gz extraction path not used on Windows")
	}

	dstDir := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "hermai.tar.gz")

	payload := []byte("#!/bin/sh\necho hermai\n")
	buf := &bytes.Buffer{}
	gz := gzip.NewWriter(buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "LICENSE", Mode: 0o644, Size: int64(len("x")), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "hermai", Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := extractBinary(archivePath, ".tar.gz", dstDir)
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("extracted bytes mismatch")
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("extracted binary not executable, mode=%v", info.Mode())
	}
}

func TestExtractBinary_Zip(t *testing.T) {
	dstDir := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "hermai.zip")

	binName := "hermai"
	if runtime.GOOS == "windows" {
		binName = "hermai.exe"
	}
	payload := []byte("fake-binary-contents")

	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	fw, err := zw.Create(binName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := extractBinary(archivePath, ".zip", dstDir)
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("extracted bytes mismatch")
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1024, "1.0KB"},
		{1024 * 1024, "1.0MB"},
		{3*1024*1024 + 512*1024, "3.5MB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
