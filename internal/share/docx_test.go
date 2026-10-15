package share

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"
)

func TestDOCXRegistered(t *testing.T) {
	s := Get("docx")
	if s == nil {
		t.Fatal("docx sharer not registered")
	}
	if s.ContentType() != docxContentType {
		t.Fatalf("unexpected content type: %q", s.ContentType())
	}
	if s.FileExtension() != ".docx" {
		t.Fatalf("unexpected extension: %q", s.FileExtension())
	}
}

func TestDOCXExportRendersMarkdown(t *testing.T) {
	pages := []Page{
		{
			Path:  "guides/start",
			Title: "Getting Started",
			Body: `# Overview

A paragraph with **bold**, *italic*, ~~deleted~~, ` + "`code`" + `, [a link](https://example.com), [[guides/next|the next guide]], and [[guides/missing|a missing guide]].

- first
- second

1. alpha
2. beta

> A useful note.

| Name | Value |
| --- | --- |
| one | two |

` + "```go\nfmt.Println(\"hello\")\n```" + `
`,
		},
		{
			Path:  "guides/next",
			Title: "Next Steps",
			Body:  "## Continue\n\nMore details.\n",
		},
	}

	var buf bytes.Buffer
	err := (&DOCXSharer{}).Export(context.Background(), &buf, ExportRequest{
		Config: ShareConfig{Settings: map[string]any{"include_toc": true}},
		Pages:  pages,
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	parts := readDOCXParts(t, buf.Bytes())
	for _, name := range []string{
		"[Content_Types].xml",
		"_rels/.rels",
		"word/document.xml",
		"word/styles.xml",
		"word/_rels/document.xml.rels",
	} {
		if _, ok := parts[name]; !ok {
			t.Errorf("missing DOCX part %s", name)
		}
	}

	for name, data := range parts {
		if !strings.HasSuffix(name, ".xml") && !strings.HasSuffix(name, ".rels") {
			continue
		}
		var value any
		if err := xml.Unmarshal(data, &value); err != nil {
			t.Errorf("invalid XML in %s: %v", name, err)
		}
	}

	doc := string(parts["word/document.xml"])
	for _, want := range []string{
		"Contents",
		"Getting Started",
		"Next Steps",
		"Overview",
		"A useful note.",
		"first",
		"second",
		"alpha",
		"beta",
		"the next guide",
		"a missing guide",
		"fmt.Println",
		`<w:tbl`,
		`w:type="page"`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("document.xml missing %q", want)
		}
	}
	if strings.Contains(doc, "[[") {
		t.Error("raw wikilink syntax should not appear in document.xml")
	}
	if !strings.Contains(doc, `w:name="_MindMapPage2"`) {
		t.Error("exported page bookmark missing")
	}
	if !strings.Contains(doc, `w:hyperlink w:anchor="_MindMapPage2"`) {
		t.Error("in-scope wikilink is not an in-document hyperlink")
	}
	if !strings.Contains(doc, `<w:color w:val="666666"></w:color>`) || !strings.Contains(doc, `<w:u w:val="single"></w:u>`) {
		t.Error("out-of-scope wikilink is not rendered as dark gray underlined text")
	}
	if count := strings.Count(doc, "<w:hyperlink"); count != 4 {
		t.Errorf("unexpected hyperlink count: got %d, want 4", count)
	}
	rels := string(parts["word/_rels/document.xml.rels"])
	if !strings.Contains(rels, `Target="https://example.com"`) || !strings.Contains(rels, `TargetMode="External"`) {
		t.Error("external link relationship missing")
	}
	if count := strings.Count(doc, "Getting Started"); count != 1 {
		t.Errorf("synthetic page title duplicated in document.xml: got %d occurrences", count)
	}
}

func TestDOCXExportEmbedsImage(t *testing.T) {
	var imageData bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: 20, G: 80, B: 160, A: 255})
		}
	}
	if err := png.Encode(&imageData, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}

	assetPath := "guides/start.assets/diagram.png"
	var buf bytes.Buffer
	err := (&DOCXSharer{}).Export(context.Background(), &buf, ExportRequest{
		Config: ShareConfig{Settings: map[string]any{"include_assets": true}},
		Pages: []Page{{
			Path:      "guides/start",
			Title:     "Guide",
			Body:      "![Architecture](" + assetPath + ")\n",
			ImageRefs: []string{assetPath},
		}},
		Assets: &mockAssetReader{assets: map[string][]byte{assetPath: imageData.Bytes()}},
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	parts := readDOCXParts(t, buf.Bytes())
	if _, ok := parts["word/media/image1.png"]; !ok {
		t.Fatal("embedded image part missing")
	}
	if !strings.Contains(string(parts["word/document.xml"]), `<w:drawing>`) ||
		!strings.Contains(string(parts["word/document.xml"]), `r:embed=`) {
		t.Error("document image drawing missing")
	}
	if !strings.Contains(string(parts["word/_rels/document.xml.rels"]), `Target="media/image1.png"`) {
		t.Error("image relationship target missing")
	}
	if !strings.Contains(string(parts["[Content_Types].xml"]), `Extension="png" ContentType="image/png"`) {
		t.Error("PNG content type missing")
	}
}

func TestDOCXExportRendersMermaidAsImage(t *testing.T) {
	if findBrowser() == "" {
		t.Skip("no Chromium-based browser on $PATH")
	}
	if findMermaidJS() == nil {
		t.Skip("mermaid.min.js not found")
	}

	var buf bytes.Buffer
	err := (&DOCXSharer{}).Export(context.Background(), &buf, ExportRequest{
		Pages: []Page{{
			Path:  "architecture/index",
			Title: "Architecture",
			Body:  "```mermaid\ngraph LR\n  A[Web UI] --> B[Go server]\n```\n",
		}},
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	parts := readDOCXParts(t, buf.Bytes())
	if _, ok := parts["word/media/image1.png"]; !ok {
		t.Fatal("rendered Mermaid image part missing")
	}
	doc := string(parts["word/document.xml"])
	if !strings.Contains(doc, `<w:drawing>`) {
		t.Error("Mermaid drawing missing from document")
	}
	if strings.Contains(doc, "graph LR") {
		t.Error("Mermaid source rendered as code instead of an image")
	}
}

func TestDOCXExportImageFallbackWhenAssetsDisabled(t *testing.T) {
	assetPath := "guides/start.assets/diagram.png"
	var buf bytes.Buffer
	err := (&DOCXSharer{}).Export(context.Background(), &buf, ExportRequest{
		Config: ShareConfig{Settings: map[string]any{"include_assets": false}},
		Pages: []Page{{
			Path:      "guides/start",
			Title:     "Guide",
			Body:      "![Architecture](" + assetPath + ")\n",
			ImageRefs: []string{assetPath},
		}},
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	parts := readDOCXParts(t, buf.Bytes())
	if !strings.Contains(string(parts["word/document.xml"]), "[Image: Architecture]") {
		t.Error("image fallback text missing")
	}
	for name := range parts {
		if strings.HasPrefix(name, "word/media/") {
			t.Errorf("unexpected media part %s", name)
		}
	}
}

func readDOCXParts(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	parts := make(map[string][]byte, len(reader.File))
	for _, file := range reader.File {
		rc, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		content, err := io.ReadAll(rc)
		closeErr := rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}
		if closeErr != nil {
			t.Fatalf("close %s: %v", file.Name, closeErr)
		}
		parts[file.Name] = content
	}
	return parts
}
