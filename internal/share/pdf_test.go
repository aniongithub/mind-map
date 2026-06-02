package share

import "testing"

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
