package share

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/chromedp/chromedp"
)

// renderMermaidPNG renders a Mermaid definition to PNG using the same local
// Mermaid bundle and Chromium discovery used by PDF export.
func renderMermaidPNG(ctx context.Context, definition string) ([]byte, error) {
	browserPath := findBrowser()
	if browserPath == "" {
		return nil, fmt.Errorf("no Chromium-based browser found")
	}
	mermaidJS := findMermaidJS()
	if mermaidJS == nil {
		return nil, fmt.Errorf("mermaid.min.js not found")
	}

	htmlContent := renderHTMLDocument([]Page{{
		Path: "diagram",
		Body: "```mermaid\n" + definition + "\n```\n",
	}}, nil, ctx, false, false, true)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(htmlContent))
	})
	mux.HandleFunc("/mermaid.min.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write(mermaidJS)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start Mermaid render server: %w", err)
	}
	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Close()
	defer listener.Close()

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)
	defer allocCancel()
	taskCtx, taskCancel := chromedp.NewContext(allocCtx)
	defer taskCancel()
	taskCtx, timeoutCancel := context.WithTimeout(taskCtx, 30*time.Second)
	defer timeoutCancel()

	pageURL := fmt.Sprintf("http://127.0.0.1:%d/", listener.Addr().(*net.TCPAddr).Port)
	var png []byte
	if err := chromedp.Run(taskCtx,
		chromedp.Navigate(pageURL),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return chromedp.Poll(`document.body && document.body.getAttribute('data-mermaid-done') === 'true'`, nil, chromedp.WithPollingInterval(100*time.Millisecond)).Do(ctx)
		}),
		chromedp.Screenshot(".mermaid", &png, chromedp.ByQuery),
	); err != nil {
		return nil, fmt.Errorf("render Mermaid diagram: %w", err)
	}
	if len(png) == 0 {
		return nil, fmt.Errorf("render Mermaid diagram: empty screenshot")
	}
	return png, nil
}
