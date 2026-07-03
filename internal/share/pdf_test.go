package share

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestPDFRegistered(t *testing.T) {
	if findBrowser() == "" {
		t.Skip("no Chromium-based browser on $PATH — PDF sharer won't register")
	}
	s := Get("pdf")
	if s == nil {
		t.Fatal("pdf sharer not registered despite browser being available")
	}
	if s.Name() != "pdf" {
		t.Errorf("expected name 'pdf', got %q", s.Name())
	}
}

func TestPDFMermaidRendering(t *testing.T) {
	if findBrowser() == "" {
		t.Skip("no Chromium-based browser on $PATH — PDF sharer won't register")
	}
	if findMermaidJS() == nil {
		t.Skip("mermaid.min.js not found — cannot test mermaid rendering")
	}

	sharer := &PDFSharer{}

	// Page with a mermaid diagram
	mermaidPage := Page{
		Path:  "test/mermaid-page",
		Title: "Mermaid Test",
		Body: `# Test Page

Here is a diagram:

` + "```mermaid\ngraph TD\n    A[Start] --> B[Process]\n    B --> C[End]\n```" + `

And some text after.
`,
	}

	ctx := context.Background()

	// Export the mermaid page to PDF
	var mermaidBuf bytes.Buffer
	err := sharer.Export(ctx, &mermaidBuf, ExportRequest{
		Pages:  []Page{mermaidPage},
		Assets: nil,
		Config: ShareConfig{Page: "test/mermaid-page", Depth: 0},
	})
	if err != nil {
		t.Fatalf("PDF export with mermaid failed: %v", err)
	}

	// Verify it's a valid PDF
	if !bytes.HasPrefix(mermaidBuf.Bytes(), []byte("%PDF")) {
		t.Fatal("mermaid PDF output doesn't start with %PDF header")
	}
	t.Logf("mermaid PDF size: %d bytes", mermaidBuf.Len())

	// Verify mermaid was rendered by checking the HTML intermediate output.
	// Use the htmlToPDF local server approach to get the rendered HTML.
	browserPath := findBrowser()
	mermaidJS := findMermaidJS()
	htmlDoc := renderHTMLDocument([]Page{mermaidPage}, nil, ctx, false, false, true)

	// Render in browser and capture the resulting HTML to verify SVG
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(htmlDoc))
	})
	mux.HandleFunc("/mermaid.min.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write(mermaidJS)
	})
	listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
	if listenErr != nil {
		t.Fatalf("failed to listen: %v", listenErr)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(listener)
	defer srv.Close()
	defer listener.Close()
	pageURL := fmt.Sprintf("http://127.0.0.1:%d/", listener.Addr().(*net.TCPAddr).Port)

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)
	defer allocCancel()
	taskCtx, taskCancel := chromedp.NewContext(allocCtx)
	defer taskCancel()
	taskCtx, timeoutCancel := context.WithTimeout(taskCtx, 30*time.Second)
	defer timeoutCancel()

	var bodyHTML string
	err = chromedp.Run(taskCtx,
		chromedp.Navigate(pageURL),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return chromedp.Poll(`document.body && document.body.getAttribute('data-mermaid-done') === 'true'`, nil, chromedp.WithPollingInterval(100*time.Millisecond)).Do(ctx)
		}),
		chromedp.OuterHTML("body", &bodyHTML),
	)
	if err != nil {
		t.Fatalf("chromedp failed: %v", err)
	}

	// The rendered HTML should contain SVG elements from mermaid
	if !strings.Contains(bodyHTML, "<svg") {
		t.Error("rendered HTML does not contain SVG — mermaid diagram was not rendered")
	}
	if !strings.Contains(bodyHTML, "flowchart") || !strings.Contains(bodyHTML, "mermaid") {
		t.Error("rendered HTML does not contain expected mermaid/flowchart class")
	}
	// The original code block should be GONE (replaced by mermaid div)
	if strings.Contains(bodyHTML, `class="language-mermaid"`) {
		t.Error("original code.language-mermaid element still present — mermaid didn't replace it")
	}
	t.Logf("rendered body HTML length: %d bytes, contains SVG: true", len(bodyHTML))
}

func TestPDFRenderHTMLContainsMermaidScript(t *testing.T) {
	// Verify that renderHTMLDocument includes the Mermaid initialization
	// script when pages contain mermaid code blocks.
	pages := []Page{
		{
			Path:  "test/page",
			Title: "Test",
			Body:  "# Hello\n\n```mermaid\ngraph TD\n    A --> B\n```\n",
		},
	}

	html := renderHTMLDocument(pages, nil, context.Background(), false, false, true)

	// Should contain mermaid.min.js script reference (local server)
	if !strings.Contains(html, "/mermaid.min.js") {
		t.Error("HTML does not contain /mermaid.min.js script reference")
	}

	// Should contain the data-mermaid-done signal
	if !strings.Contains(html, "data-mermaid-done") {
		t.Error("HTML does not contain data-mermaid-done signal")
	}

	// Goldmark should have rendered the mermaid block as a code element
	if !strings.Contains(html, `class="language-mermaid"`) {
		t.Error("HTML does not contain language-mermaid code block (goldmark output)")
	}

	// Should contain the graph definition text
	if !strings.Contains(html, "A --&gt; B") || !strings.Contains(html, "graph TD") {
		// goldmark may or may not HTML-escape inside code blocks
		if !strings.Contains(html, "A --> B") && !strings.Contains(html, "A --&gt; B") {
			t.Error("HTML does not contain the mermaid graph definition")
		}
	}
}

func TestPDFRenderHTMLNoMermaid(t *testing.T) {
	// When hasMermaid is false, no mermaid script should be included
	pages := []Page{
		{
			Path:  "test/page",
			Title: "Test",
			Body:  "# Hello\n\nJust text.\n",
		},
	}

	html := renderHTMLDocument(pages, nil, context.Background(), false, false, false)

	if strings.Contains(html, "/mermaid.min.js") {
		t.Error("HTML should not contain mermaid script when hasMermaid is false")
	}
	// Should still have the done signal for the wait loop
	if !strings.Contains(html, "data-mermaid-done") {
		t.Error("HTML should still contain data-mermaid-done signal for consistency")
	}
}
