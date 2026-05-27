package pdftext

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestExtractFixturePDF(t *testing.T) {
	data, err := os.ReadFile("../../testdata/illinois-2026-02enf.pdf")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Extract(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if doc.ContentType != "application/pdf" {
		t.Fatalf("content type = %q, want application/pdf", doc.ContentType)
	}
	if doc.PageCount != 24 {
		t.Fatalf("page count = %d, want 24", doc.PageCount)
	}
	if doc.OCRRequired {
		t.Fatal("fixture should not require OCR")
	}
	for _, page := range doc.Pages {
		if page.OCRRequired {
			t.Fatalf("page %d should not require OCR", page.Number)
		}
	}
	if !strings.Contains(doc.Text, "February 2026 Enforcement Actions") {
		t.Fatal("expected extracted text to include report title")
	}
}

func TestIsPDF(t *testing.T) {
	if !IsPDF([]byte("%PDF-1.7\n")) {
		t.Fatal("expected PDF magic header to be detected")
	}
	if IsPDF([]byte("<html></html>")) {
		t.Fatal("did not expect HTML to be detected as PDF")
	}
}
