package pdftext

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var ErrNotPDF = errors.New("input is not a PDF")

type Page struct {
	Number      int    `json:"page"`
	Text        string `json:"text"`
	OCRRequired bool   `json:"ocr_required,omitempty"`
}

type Document struct {
	ContentType string `json:"content_type"`
	PageCount   int    `json:"page_count"`
	Text        string `json:"text"`
	Pages       []Page `json:"pages"`
	OCRRequired bool   `json:"ocr_required,omitempty"`
}

func IsPDF(data []byte) bool {
	return bytes.HasPrefix(bytes.TrimSpace(data), []byte("%PDF-"))
}

func Extract(ctx context.Context, data []byte) (*Document, error) {
	if !IsPDF(data) {
		return nil, ErrNotPDF
	}

	if _, err := exec.LookPath("pdftotext"); err != nil {
		return nil, fmt.Errorf("pdftotext is required for PDF extraction: %w", err)
	}

	f, err := os.CreateTemp("", "hermai-*.pdf")
	if err != nil {
		return nil, fmt.Errorf("create temp PDF: %w", err)
	}
	defer os.Remove(f.Name())

	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, fmt.Errorf("write temp PDF: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close temp PDF: %w", err)
	}

	cmd := exec.CommandContext(ctx, "pdftotext", "-layout", "-enc", "UTF-8", f.Name(), "-")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("pdftotext failed: %w", err)
	}

	text := normalizeText(string(out))
	pages := splitPages(text)
	ocrRequired := false
	for _, page := range pages {
		if page.OCRRequired {
			ocrRequired = true
			break
		}
	}
	return &Document{
		ContentType: "application/pdf",
		PageCount:   len(pages),
		Text:        text,
		Pages:       pages,
		OCRRequired: ocrRequired,
	}, nil
}

func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.TrimSpace(s)
}

func splitPages(text string) []Page {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	parts := strings.Split(text, "\f")
	pages := make([]Page, 0, len(parts))
	for i, part := range parts {
		trimmed := strings.TrimSpace(part)
		if i == len(parts)-1 && trimmed == "" {
			continue
		}
		pages = append(pages, Page{
			Number:      i + 1,
			Text:        trimmed,
			OCRRequired: trimmed == "",
		})
	}
	return pages
}
