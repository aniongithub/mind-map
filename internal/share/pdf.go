package share

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer"
	goldhtml "github.com/yuin/goldmark/renderer/html"
)

// PDFSharer exports wiki pages as a multi-page PDF with a table of contents,
// rendered via a Chromium-based browser (Chrome, Edge, or Chromium).
type PDFSharer struct{}

func init() {
	// Only register if a Chromium-based browser is available on the system.
	if findBrowser() != "" {
		Register(&PDFSharer{})
	}
}

func (p *PDFSharer) Name() string        { return "pdf" }
func (p *PDFSharer) Description() string  { return "Multi-page PDF with table of contents (requires Chrome/Edge)" }
func (p *PDFSharer) ContentType() string  { return "application/pdf" }
func (p *PDFSharer) FileExtension() string { return ".pdf" }

func (p *PDFSharer) Settings() SharerSettings {
	return SharerSettings{
		Fields: []SettingsField{
			{
				Key:         "include_toc",
				Label:       "Include table of contents",
				Description: "Generate a clickable table of contents at the beginning",
				Type:        "bool",
				Default:     true,
			},
			{
				Key:         "include_assets",
				Label:       "Embed images",
				Description: "Embed referenced images inline in the PDF",
				Type:        "bool",
				Default:     true,
			},
			{
				Key:         "page_size",
				Label:       "Page size",
				Description: "Paper size for the PDF",
				Type:        "enum",
				Default:     "A4",
				Enum:        []string{"A4", "Letter", "A3", "Legal"},
			},
		},
	}
}

func (p *PDFSharer) Export(ctx context.Context, w io.Writer, req ExportRequest) error {
	browserPath := findBrowser()
	if browserPath == "" {
		return fmt.Errorf("no Chromium-based browser found (need Chrome, Edge, or Chromium)")
	}

	// Extract settings
	includeTOC := SettingBool(req.Config, "include_toc", true)
	includeAssets := SettingBool(req.Config, "include_assets", true)
	pageSize := SettingString(req.Config, "page_size", "A4")

	// Detect mermaid blocks in any page and load mermaid JS if needed
	hasMermaid := pagesHaveMermaid(req.Pages)
	var mermaidJS []byte
	if hasMermaid {
		mermaidJS = findMermaidJS()
		if mermaidJS == nil {
			hasMermaid = false // degrade gracefully — render code blocks as-is
		}
	}

	// Render pages to HTML
	htmlDoc := renderHTMLDocument(req.Pages, req.Assets, ctx, includeTOC, includeAssets, hasMermaid)

	// Convert to PDF via headless browser
	pdfBytes, err := htmlToPDF(ctx, browserPath, htmlDoc, pageSize, mermaidJS)
	if err != nil {
		return fmt.Errorf("PDF generation failed: %w", err)
	}

	_, err = w.Write(pdfBytes)
	return err
}

// renderHTMLDocument builds a complete HTML document from the exported pages.
// If hasMermaid is true, includes a script tag that loads mermaid from /mermaid.min.js
// (served by the local HTTP server in htmlToPDF).
func renderHTMLDocument(pages []Page, assets AssetReader, ctx context.Context, includeTOC, includeAssets, hasMermaid bool) string {
	var buf strings.Builder

	buf.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8">`)
	buf.WriteString(`<style>`)
	buf.WriteString(pdfCSS)
	buf.WriteString(`</style>`)
	if hasMermaid {
		// Load mermaid from local server, then render code blocks to SVG
		buf.WriteString(`<script src="/mermaid.min.js"></script>`)
		buf.WriteString(`<script>`)
		buf.WriteString(`mermaid.initialize({ startOnLoad: false, theme: 'default' });`)
		buf.WriteString(`window.addEventListener('DOMContentLoaded', async function() {`)
		buf.WriteString(`  var nodes = document.querySelectorAll('code.language-mermaid');`)
		buf.WriteString(`  for (var i = 0; i < nodes.length; i++) {`)
		buf.WriteString(`    var pre = nodes[i].parentElement;`)
		buf.WriteString(`    var container = document.createElement('div');`)
		buf.WriteString(`    container.className = 'mermaid';`)
		buf.WriteString(`    container.textContent = nodes[i].textContent;`)
		buf.WriteString(`    pre.replaceWith(container);`)
		buf.WriteString(`  }`)
		buf.WriteString(`  var mermaidNodes = document.querySelectorAll('.mermaid');`)
		buf.WriteString(`  if (mermaidNodes.length > 0) {`)
		buf.WriteString(`    await mermaid.run({ nodes: mermaidNodes });`)
		buf.WriteString(`  }`)
		buf.WriteString(`  document.body.setAttribute('data-mermaid-done', 'true');`)
		buf.WriteString(`});`)
		buf.WriteString(`</script>`)
	}
	buf.WriteString(`</head><body>`)

	// If no mermaid, immediately mark done for the wait loop
	if !hasMermaid {
		buf.WriteString(`<script>document.body.setAttribute('data-mermaid-done','true');</script>`)
	}

	// Table of contents
	if includeTOC && len(pages) > 1 {
		buf.WriteString(`<div class="toc"><h1>Contents</h1><ul>`)
		for i, p := range pages {
			title := p.Title
			if title == "" {
				title = p.Path
			}
			buf.WriteString(fmt.Sprintf(`<li><a href="#page-%d">%s</a></li>`,
				i, html.EscapeString(title)))
		}
		buf.WriteString(`</ul></div>`)
	}

	// Render each page
	md := goldmark.New(
		goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.TaskList),
		goldmark.WithRendererOptions(renderer.WithNodeRenderers(), goldhtml.WithUnsafe()),
	)

	for i, p := range pages {
		buf.WriteString(fmt.Sprintf(`<article id="page-%d">`, i))

		// Page title
		title := p.Title
		if title == "" {
			title = p.Path
		}
		buf.WriteString(fmt.Sprintf(`<h1 class="page-title">%s</h1>`, html.EscapeString(title)))
		buf.WriteString(fmt.Sprintf(`<div class="page-path">%s</div>`, html.EscapeString(p.Path)))

		// Render markdown body to HTML
		body := p.Body

		// If including assets, replace image references with data URIs
		if includeAssets && assets != nil {
			body = embedImages(body, p.ImageRefs, assets, ctx)
		}

		var mdBuf bytes.Buffer
		if err := md.Convert([]byte(body), &mdBuf); err != nil {
			slog.Warn("pdf: markdown render failed", slog.String("page", p.Path), slog.Any("error", err))
			buf.WriteString(fmt.Sprintf(`<pre>%s</pre>`, html.EscapeString(body)))
		} else {
			buf.WriteString(mdBuf.String())
		}

		buf.WriteString(`</article>`)
	}

	buf.WriteString(`</body></html>`)
	return buf.String()
}

// embedImages replaces markdown image references with base64 data URIs.
func embedImages(body string, imageRefs []string, assets AssetReader, ctx context.Context) string {
	for _, ref := range imageRefs {
		content, mime, err := assets.ReadAsset(ctx, ref)
		if err != nil {
			slog.Warn("pdf: failed to read asset", slog.String("ref", ref), slog.Any("error", err))
			continue
		}
		// Build data URI
		dataURI := fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(content))

		// Replace the reference in the markdown body.
		// Image refs appear as relative paths in markdown: ![alt](path)
		// We need to find the markdown image syntax referencing this asset.
		body = strings.ReplaceAll(body, ref, dataURI)
	}
	return body
}

// pagesHaveMermaid returns true if any page body contains a mermaid fenced code block.
func pagesHaveMermaid(pages []Page) bool {
	for _, p := range pages {
		if strings.Contains(p.Body, "```mermaid") {
			return true
		}
	}
	return false
}

// findMermaidJS locates and reads the mermaid.min.js bundle.
// It checks common locations relative to the working directory and executable.
func findMermaidJS() []byte {
	candidates := []string{
		// Development: relative to project root (cwd)
		"webui/node_modules/mermaid/dist/mermaid.min.js",
		// Two levels up from internal/share/ (tests run from package dir)
		"../../webui/node_modules/mermaid/dist/mermaid.min.js",
	}

	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate)
		if err == nil {
			return data
		}
	}

	// Try relative to the executable (production: mermaid.min.js next to binary)
	if exePath, err := os.Executable(); err == nil {
		dir := filepath.Dir(exePath)
		for _, rel := range []string{
			filepath.Join(dir, "mermaid.min.js"),
			filepath.Join(dir, "webui", "node_modules", "mermaid", "dist", "mermaid.min.js"),
		} {
			if data, err := os.ReadFile(rel); err == nil {
				return data
			}
		}
	}

	return nil
}

// htmlToPDF uses chromedp to render HTML to PDF.
func htmlToPDF(ctx context.Context, browserPath, htmlContent, pageSize string, mermaidJS []byte) ([]byte, error) {
	// Start a local HTTP server to serve the HTML content.
	// This gives the page a proper origin so scripts and local resources work.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(htmlContent))
	})
	if mermaidJS != nil {
		mux.HandleFunc("/mermaid.min.js", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/javascript")
			w.Write(mermaidJS)
		})
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to start local server: %w", err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(listener)
	defer srv.Close()
	defer listener.Close()
	pageURL := fmt.Sprintf("http://127.0.0.1:%d/", listener.Addr().(*net.TCPAddr).Port)

	// Create a context with the browser path
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)
	defer allocCancel()

	taskCtx, taskCancel := chromedp.NewContext(allocCtx)
	defer taskCancel()

	// Set a reasonable timeout
	taskCtx, timeoutCancel := context.WithTimeout(taskCtx, 60*time.Second)
	defer timeoutCancel()

	// Navigate to the HTML content and print to PDF
	var pdfBuf []byte
	err = chromedp.Run(taskCtx,
		chromedp.Navigate(pageURL),
		// Wait for Mermaid diagrams to finish rendering.
		// The mermaid init script sets data-mermaid-done="true" on <body>
		// after all diagrams render (or on error). Poll until it appears.
		chromedp.ActionFunc(func(ctx context.Context) error {
			return chromedp.Poll(`document.body && document.body.getAttribute('data-mermaid-done') === 'true'`, nil, chromedp.WithPollingInterval(100*time.Millisecond)).Do(ctx)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			paperWidth, paperHeight := paperDimensions(pageSize)
			var err error
			pdfBuf, _, err = page.PrintToPDF().
				WithPaperWidth(paperWidth).
				WithPaperHeight(paperHeight).
				WithMarginTop(0.5).
				WithMarginBottom(0.5).
				WithMarginLeft(0.5).
				WithMarginRight(0.5).
				WithPrintBackground(true).
				Do(ctx)
			return err
		}),
	)
	if err != nil {
		return nil, err
	}

	return pdfBuf, nil
}

// paperDimensions returns width and height in inches for the given page size.
func paperDimensions(size string) (width, height float64) {
	switch strings.ToLower(size) {
	case "letter":
		return 8.5, 11
	case "legal":
		return 8.5, 14
	case "a3":
		return 11.69, 16.54
	default: // A4
		return 8.27, 11.69
	}
}

// findBrowser scans for a Chromium-based browser on the system.
func findBrowser() string {
	// Platform-specific paths to try
	var candidates []string

	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
	case "windows":
		candidates = []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
		}
	default: // linux and others
		candidates = []string{}
	}

	// Also check $PATH for common names
	pathNames := []string{
		"google-chrome",
		"google-chrome-stable",
		"chromium",
		"chromium-browser",
		"microsoft-edge",
		"microsoft-edge-stable",
		"brave-browser",
	}

	for _, name := range pathNames {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}

	for _, path := range candidates {
		if _, err := exec.LookPath(path); err == nil {
			return path
		}
	}

	return ""
}

// pdfCSS is the stylesheet used for PDF rendering.
const pdfCSS = `
body {
	font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
	font-size: 12pt;
	line-height: 1.6;
	color: #1a1a1a;
	max-width: 100%;
}

.toc {
	page-break-after: always;
}
.toc h1 {
	font-size: 24pt;
	margin-bottom: 16pt;
}
.toc ul {
	list-style: none;
	padding: 0;
}
.toc li {
	padding: 4pt 0;
	border-bottom: 1px solid #eee;
}
.toc a {
	color: #0366d6;
	text-decoration: none;
}

article {
	page-break-before: always;
}
article:first-of-type {
	page-break-before: auto;
}

.page-title {
	font-size: 20pt;
	margin-bottom: 4pt;
	color: #111;
}
.page-path {
	font-size: 9pt;
	color: #666;
	font-family: monospace;
	margin-bottom: 16pt;
	padding-bottom: 8pt;
	border-bottom: 1px solid #ddd;
}

h1, h2, h3, h4 {
	page-break-after: avoid;
}
h1 { font-size: 18pt; }
h2 { font-size: 15pt; }
h3 { font-size: 13pt; }

pre, code {
	font-family: "SF Mono", "Fira Code", monospace;
	font-size: 10pt;
}
pre {
	background: #f6f8fa;
	padding: 12pt;
	border-radius: 4pt;
	overflow-wrap: break-word;
	white-space: pre-wrap;
	page-break-inside: avoid;
}
code {
	background: #f0f0f0;
	padding: 1pt 4pt;
	border-radius: 2pt;
}
pre code {
	background: none;
	padding: 0;
}

table {
	border-collapse: collapse;
	width: 100%;
	margin: 12pt 0;
	page-break-inside: avoid;
}
th, td {
	border: 1px solid #ddd;
	padding: 6pt 10pt;
	text-align: left;
}
th {
	background: #f6f8fa;
	font-weight: 600;
}

img {
	max-width: 100%;
	height: auto;
	page-break-inside: avoid;
}

blockquote {
	border-left: 3pt solid #ddd;
	margin-left: 0;
	padding-left: 12pt;
	color: #555;
}

a {
	color: #0366d6;
}
`
