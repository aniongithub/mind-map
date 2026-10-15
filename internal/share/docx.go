package share

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"

	docx "github.com/mmonterroca/docxgo/v2"
	"github.com/mmonterroca/docxgo/v2/domain"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

const docxContentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// DOCXSharer exports wiki pages as a Microsoft Word document.
type DOCXSharer struct{}

func init() {
	Register(&DOCXSharer{})
}

func (d *DOCXSharer) Name() string { return "docx" }
func (d *DOCXSharer) Description() string {
	return "Microsoft Word document with rendered Markdown and embedded images"
}
func (d *DOCXSharer) ContentType() string   { return docxContentType }
func (d *DOCXSharer) FileExtension() string { return ".docx" }

func (d *DOCXSharer) Settings() SharerSettings {
	return SharerSettings{Fields: []SettingsField{
		{
			Key:         "include_toc",
			Label:       "Include table of contents",
			Description: "Add a contents section when exporting multiple pages",
			Type:        "bool",
			Default:     true,
		},
		{
			Key:         "include_assets",
			Label:       "Embed images",
			Description: "Embed referenced PNG, JPEG, and GIF images in the document",
			Type:        "bool",
			Default:     true,
		},
	}}
}

func (d *DOCXSharer) Export(ctx context.Context, w io.Writer, req ExportRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	document := docx.NewDocument()
	renderer := &docxRenderer{
		ctx:           ctx,
		document:      document,
		assets:        req.Assets,
		includeAssets: SettingBool(req.Config, "include_assets", true),
	}
	if err := renderer.render(req.Pages, SettingBool(req.Config, "include_toc", true)); err != nil {
		return err
	}
	if _, err := document.WriteTo(w); err != nil {
		return fmt.Errorf("write DOCX document: %w", err)
	}
	return nil
}

type docxTextStyle struct {
	bold           bool
	italic         bool
	strike         bool
	code           bool
	linkTarget     string
	outOfScopeWiki bool
}

type bookmarkSetter interface {
	SetBookmark(id, name string)
}

type docxRenderer struct {
	ctx               context.Context
	document          domain.Document
	assets            AssetReader
	includeAssets     bool
	source            []byte
	page              Page
	pageAnchors       map[string]string
	pendingPageAnchor string
	bookmarkID        int
}

func (r *docxRenderer) render(pages []Page, includeTOC bool) error {
	r.pageAnchors = make(map[string]string, len(pages))
	r.bookmarkID = 100000
	for i, page := range pages {
		r.pageAnchors[normalizePagePath(page.Path)] = fmt.Sprintf("_MindMapPage%d", i+1)
	}

	if includeTOC && len(pages) > 1 {
		if _, err := r.addParagraph(domain.StyleIDHeading1, "Contents"); err != nil {
			return fmt.Errorf("add DOCX contents heading: %w", err)
		}
		for _, page := range pages {
			title := page.Title
			if title == "" {
				title = page.Path
			}
			paragraph, err := r.addParagraph(domain.StyleIDListParagraph, "")
			if err != nil {
				return fmt.Errorf("add DOCX contents entry: %w", err)
			}
			if err := r.addStyledText(paragraph, "• ", docxTextStyle{}); err != nil {
				return err
			}
			if err := r.addStyledText(paragraph, title, docxTextStyle{linkTarget: "#" + r.pageAnchors[normalizePagePath(page.Path)]}); err != nil {
				return err
			}
		}
	}

	for i, page := range pages {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if i > 0 || (includeTOC && len(pages) > 1) {
			if err := r.document.AddPageBreak(); err != nil {
				return fmt.Errorf("add DOCX page break: %w", err)
			}
		}

		r.page = page
		r.pendingPageAnchor = r.pageAnchors[normalizePagePath(page.Path)]
		r.source = []byte(rewriteWikilinks(page.Body))
		md := goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.TaskList))
		parsed := md.Parser().Parse(text.NewReader(r.source))
		if err := r.renderBlocks(parsed, 0, false); err != nil {
			return fmt.Errorf("render DOCX page %q: %w", page.Path, err)
		}
		if r.pendingPageAnchor != "" {
			if _, err := r.addParagraph(domain.StyleIDNormal, ""); err != nil {
				return fmt.Errorf("add DOCX bookmark for empty page %q: %w", page.Path, err)
			}
		}
	}
	return nil
}

func (r *docxRenderer) addParagraph(style, value string) (domain.Paragraph, error) {
	paragraph, err := r.document.AddParagraph()
	if err != nil {
		return nil, err
	}
	if style != "" {
		if err := paragraph.SetStyle(style); err != nil {
			return nil, err
		}
	}
	if err := r.attachPageBookmark(paragraph, style); err != nil {
		return nil, err
	}
	if value != "" {
		if err := r.addStyledText(paragraph, value, docxTextStyle{}); err != nil {
			return nil, err
		}
	}
	return paragraph, nil
}

func (r *docxRenderer) attachPageBookmark(paragraph domain.Paragraph, style string) error {
	if r.pendingPageAnchor == "" || strings.HasPrefix(style, "Heading") {
		return nil
	}
	bookmarkable, ok := paragraph.(bookmarkSetter)
	if !ok {
		return fmt.Errorf("DOCX paragraph does not support bookmarks")
	}
	r.bookmarkID++
	bookmarkable.SetBookmark(fmt.Sprint(r.bookmarkID), r.pendingPageAnchor)
	r.pendingPageAnchor = ""
	return nil
}

func (r *docxRenderer) renderBlocks(parent ast.Node, listLevel int, quote bool) error {
	for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		switch n := node.(type) {
		case *ast.Heading:
			level := min(n.Level, 6)
			paragraph, err := r.addParagraph(fmt.Sprintf("Heading%d", level), "")
			if err != nil {
				return err
			}
			if err := r.renderInlines(paragraph, n, docxTextStyle{}); err != nil {
				return err
			}
		case *ast.Paragraph:
			style := domain.StyleIDNormal
			if quote {
				style = domain.StyleIDIntenseQuote
			}
			paragraph, err := r.addParagraph(style, "")
			if err != nil {
				return err
			}
			if err := r.renderInlines(paragraph, n, docxTextStyle{}); err != nil {
				return err
			}
		case *ast.List:
			if err := r.renderList(n, listLevel); err != nil {
				return err
			}
		case *ast.Blockquote:
			if err := r.renderBlocks(n, listLevel, true); err != nil {
				return err
			}
		case *ast.CodeBlock:
			if err := r.addCodeBlock(string(n.Text(r.source))); err != nil {
				return err
			}
		case *ast.FencedCodeBlock:
			if r.isMermaidBlock(n) {
				if err := r.addMermaidDiagram(string(n.Text(r.source))); err != nil {
					return err
				}
			} else if err := r.addCodeBlock(string(n.Text(r.source))); err != nil {
				return err
			}
		case *ast.ThematicBreak:
			if _, err := r.addParagraph(domain.StyleIDNormal, "―"); err != nil {
				return err
			}
		case *extast.Table:
			if err := r.addTable(n); err != nil {
				return err
			}
		case *ast.TextBlock:
			paragraph, err := r.addParagraph(domain.StyleIDNormal, "")
			if err != nil {
				return err
			}
			if err := r.renderInlines(paragraph, n, docxTextStyle{}); err != nil {
				return err
			}
		default:
			if n.Type() == ast.TypeBlock && n.HasChildren() {
				if err := r.renderBlocks(n, listLevel, quote); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (r *docxRenderer) renderList(list *ast.List, level int) error {
	itemNumber := list.Start
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		listItem, ok := item.(*ast.ListItem)
		if !ok {
			continue
		}
		firstBlock := true
		for child := listItem.FirstChild(); child != nil; child = child.NextSibling() {
			switch n := child.(type) {
			case *ast.Paragraph, *ast.TextBlock:
				paragraph, err := r.addParagraph(domain.StyleIDListParagraph, "")
				if err != nil {
					return err
				}
				if err := paragraph.SetIndentLeft((level + 1) * 360); err != nil {
					return err
				}
				if firstBlock {
					marker := "• "
					if list.IsOrdered() {
						marker = fmt.Sprintf("%d. ", itemNumber)
					}
					if err := r.addStyledText(paragraph, marker, docxTextStyle{}); err != nil {
						return err
					}
				}
				if err := r.renderInlines(paragraph, n, docxTextStyle{}); err != nil {
					return err
				}
				firstBlock = false
			case *ast.List:
				if err := r.renderList(n, level+1); err != nil {
					return err
				}
			case *ast.Blockquote:
				if err := r.renderBlocks(n, level, true); err != nil {
					return err
				}
			case *ast.CodeBlock:
				if err := r.addCodeBlock(string(n.Text(r.source))); err != nil {
					return err
				}
			case *ast.FencedCodeBlock:
				if r.isMermaidBlock(n) {
					if err := r.addMermaidDiagram(string(n.Text(r.source))); err != nil {
						return err
					}
				} else if err := r.addCodeBlock(string(n.Text(r.source))); err != nil {
					return err
				}
			case *extast.Table:
				if err := r.addTable(n); err != nil {
					return err
				}
			}
		}
		itemNumber++
	}
	return nil
}

func (r *docxRenderer) addCodeBlock(code string) error {
	code = strings.TrimSuffix(code, "\n")
	for _, line := range strings.Split(code, "\n") {
		paragraph, err := r.addParagraph(domain.StyleIDNoSpacing, "")
		if err != nil {
			return err
		}
		if err := r.addStyledText(paragraph, line, docxTextStyle{code: true}); err != nil {
			return err
		}
	}
	return nil
}

func (r *docxRenderer) isMermaidBlock(block *ast.FencedCodeBlock) bool {
	return strings.EqualFold(strings.TrimSpace(string(block.Language(r.source))), "mermaid")
}

func (r *docxRenderer) addMermaidDiagram(definition string) error {
	png, err := renderMermaidPNG(r.ctx, definition)
	if err != nil {
		slog.Warn("docx: Mermaid render failed; using code fallback",
			slog.String("page", r.page.Path),
			slog.Any("error", err),
		)
		return r.addCodeBlock(definition)
	}
	paragraph, err := r.addParagraph(domain.StyleIDNormal, "")
	if err != nil {
		return err
	}
	if err := r.addPictureBytes(paragraph, png, domain.ImageFormatPNG, "Mermaid diagram"); err != nil {
		slog.Warn("docx: Mermaid image embed failed; using code fallback",
			slog.String("page", r.page.Path),
			slog.Any("error", err),
		)
		return r.addCodeBlock(definition)
	}
	return nil
}

func (r *docxRenderer) addTable(tableNode *extast.Table) error {
	var rows []ast.Node
	maxColumns := 0
	for rowNode := tableNode.FirstChild(); rowNode != nil; rowNode = rowNode.NextSibling() {
		switch rowNode.(type) {
		case *extast.TableHeader, *extast.TableRow:
			rows = append(rows, rowNode)
			columns := 0
			for cell := rowNode.FirstChild(); cell != nil; cell = cell.NextSibling() {
				if _, ok := cell.(*extast.TableCell); ok {
					columns++
				}
			}
			maxColumns = max(maxColumns, columns)
		}
	}
	if len(rows) == 0 || maxColumns == 0 {
		return nil
	}

	table, err := r.document.AddTable(len(rows), maxColumns)
	if err != nil {
		return err
	}
	if err := table.SetStyle(domain.TableStyleAccent1); err != nil {
		return err
	}
	for rowIndex, rowNode := range rows {
		header := false
		if _, ok := rowNode.(*extast.TableHeader); ok {
			header = true
		}
		row, err := table.Row(rowIndex)
		if err != nil {
			return err
		}
		column := 0
		for cellNode := rowNode.FirstChild(); cellNode != nil; cellNode = cellNode.NextSibling() {
			if _, ok := cellNode.(*extast.TableCell); !ok {
				continue
			}
			cell, err := row.Cell(column)
			if err != nil {
				return err
			}
			paragraph, err := cell.AddParagraph()
			if err != nil {
				return err
			}
			if err := r.attachPageBookmark(paragraph, domain.StyleIDNormal); err != nil {
				return err
			}
			if err := r.renderInlines(paragraph, cellNode, docxTextStyle{bold: header}); err != nil {
				return err
			}
			column++
		}
	}
	return nil
}

func (r *docxRenderer) renderInlines(paragraph domain.Paragraph, parent ast.Node, style docxTextStyle) error {
	for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
		switch n := node.(type) {
		case *ast.Text:
			if err := r.addStyledText(paragraph, string(n.Segment.Value(r.source)), style); err != nil {
				return err
			}
			if n.HardLineBreak() || n.SoftLineBreak() {
				if err := r.addStyledText(paragraph, " ", style); err != nil {
					return err
				}
			}
		case *ast.String:
			if err := r.addStyledText(paragraph, string(n.Value), style); err != nil {
				return err
			}
		case *ast.CodeSpan:
			next := style
			next.code = true
			if err := r.renderInlines(paragraph, n, next); err != nil {
				return err
			}
		case *ast.Emphasis:
			next := style
			if n.Level == 2 {
				next.bold = true
			} else {
				next.italic = true
			}
			if err := r.renderInlines(paragraph, n, next); err != nil {
				return err
			}
		case *extast.Strikethrough:
			next := style
			next.strike = true
			if err := r.renderInlines(paragraph, n, next); err != nil {
				return err
			}
		case *ast.Link:
			next := style
			next.linkTarget, next.outOfScopeWiki = r.resolveLink(string(n.Destination))
			if err := r.renderInlines(paragraph, n, next); err != nil {
				return err
			}
		case *ast.AutoLink:
			destination := string(n.URL(r.source))
			target, outOfScope := r.resolveLink(destination)
			if err := r.addStyledText(paragraph, destination, docxTextStyle{linkTarget: target, outOfScopeWiki: outOfScope}); err != nil {
				return err
			}
		case *ast.Image:
			if err := r.addImage(paragraph, string(n.Destination), r.inlinePlainText(n)); err != nil {
				return err
			}
		case *extast.TaskCheckBox:
			mark := "☐ "
			if n.IsChecked {
				mark = "☒ "
			}
			if err := r.addStyledText(paragraph, mark, style); err != nil {
				return err
			}
		default:
			if n.HasChildren() {
				if err := r.renderInlines(paragraph, n, style); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (r *docxRenderer) resolveLink(destination string) (target string, outOfScopeWiki bool) {
	if strings.HasPrefix(destination, "mind-map:") {
		path, err := url.PathUnescape(strings.TrimPrefix(destination, "mind-map:"))
		if err != nil {
			return "", true
		}
		anchor, ok := r.pageAnchors[normalizePagePath(path)]
		if !ok {
			return "", true
		}
		return "#" + anchor, false
	}
	if isExternalLink(destination) {
		return destination, false
	}
	return "", false
}

func (r *docxRenderer) addStyledText(paragraph domain.Paragraph, value string, style docxTextStyle) error {
	if value == "" {
		return nil
	}
	run, err := paragraph.AddRun()
	if err != nil {
		return err
	}
	if err := run.SetText(value); err != nil {
		return err
	}
	if err := run.SetBold(style.bold); err != nil {
		return err
	}
	if err := run.SetItalic(style.italic); err != nil {
		return err
	}
	if err := run.SetStrike(style.strike); err != nil {
		return err
	}
	if style.code {
		if err := run.SetFont(domain.Font{Name: "Consolas"}); err != nil {
			return err
		}
		if err := run.SetHighlight(domain.HighlightLightGray); err != nil {
			return err
		}
	}
	if style.outOfScopeWiki {
		if err := run.SetColor(domain.Color{R: 0x66, G: 0x66, B: 0x66}); err != nil {
			return err
		}
		return run.SetUnderline(domain.UnderlineSingle)
	}
	if style.linkTarget != "" {
		field := docx.NewHyperlinkField(style.linkTarget, value)
		if err := run.SetColor(domain.Color{R: 0x05, G: 0x63, B: 0xC1}); err != nil {
			return err
		}
		if err := run.SetUnderline(domain.UnderlineSingle); err != nil {
			return err
		}
		if err := run.AddField(field); err != nil {
			return err
		}
	}
	return nil
}

func (r *docxRenderer) addImage(paragraph domain.Paragraph, destination, alt string) error {
	fallback := alt
	if fallback == "" {
		fallback = filepath.Base(destination)
	}
	assetPath := r.resolveImagePath(destination)
	if !r.includeAssets || r.assets == nil || assetPath == "" {
		return r.addStyledText(paragraph, "[Image: "+fallback+"]", docxTextStyle{italic: true})
	}

	content, mimeType, err := r.assets.ReadAsset(r.ctx, assetPath)
	if err != nil {
		return r.addStyledText(paragraph, "[Image: "+fallback+"]", docxTextStyle{italic: true})
	}
	format := docxImageFormat(mimeType)
	if format == "" {
		return r.addStyledText(paragraph, "[Image: "+fallback+"]", docxTextStyle{italic: true})
	}
	if err := r.addPictureBytes(paragraph, content, format, assetPath); err != nil {
		return r.addStyledText(paragraph, "[Image: "+fallback+"]", docxTextStyle{italic: true})
	}
	return nil
}

func (r *docxRenderer) addPictureBytes(paragraph domain.Paragraph, content []byte, format domain.ImageFormat, name string) error {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		return fmt.Errorf("read DOCX image dimensions for %q: %w", name, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return fmt.Errorf("read DOCX image dimensions for %q: invalid dimensions", name)
	}

	width := float64(cfg.Width) / 96
	height := float64(cfg.Height) / 96
	if width > 6.5 {
		height *= 6.5 / width
		width = 6.5
	}
	if height > 8 {
		width *= 8 / height
		height = 8
	}
	picture, err := paragraph.AddImageFromBytesWithSize(content, format, domain.NewImageSizeInches(width, height))
	if err != nil {
		return fmt.Errorf("embed DOCX image %q: %w", name, err)
	}
	if err := picture.SetDescription(name); err != nil {
		return fmt.Errorf("set DOCX image description for %q: %w", name, err)
	}
	return nil
}

func (r *docxRenderer) resolveImagePath(destination string) string {
	clean := strings.TrimPrefix(destination, "/assets/")
	clean = strings.TrimPrefix(clean, "assets/")
	for _, ref := range r.page.ImageRefs {
		if ref == destination || ref == clean {
			return ref
		}
	}
	return ""
}

func (r *docxRenderer) inlinePlainText(parent ast.Node) string {
	var out strings.Builder
	_ = ast.Walk(parent, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.Text:
			out.Write(n.Segment.Value(r.source))
		case *ast.String:
			out.Write(n.Value)
		}
		return ast.WalkContinue, nil
	})
	return out.String()
}

func rewriteWikilinks(body string) string {
	var out strings.Builder
	for {
		start := strings.Index(body, "[[")
		if start < 0 {
			out.WriteString(body)
			break
		}
		end := strings.Index(body[start+2:], "]]")
		if end < 0 {
			out.WriteString(body)
			break
		}

		out.WriteString(body[:start])
		raw := strings.TrimSpace(body[start+2 : start+2+end])
		target := raw
		label := raw
		if pipe := strings.Index(raw, "|"); pipe >= 0 {
			target = strings.TrimSpace(raw[:pipe])
			label = strings.TrimSpace(raw[pipe+1:])
		}
		if target == "" || label == "" {
			out.WriteString(body[start : start+2+end+2])
		} else {
			label = strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`).Replace(label)
			out.WriteString("[")
			out.WriteString(label)
			out.WriteString("](mind-map:")
			out.WriteString(url.PathEscape(target))
			out.WriteString(")")
		}
		body = body[start+2+end+2:]
	}
	return out.String()
}

func normalizePagePath(path string) string {
	path = strings.TrimSpace(strings.TrimPrefix(path, "/"))
	return strings.TrimSuffix(path, ".md")
}

func isExternalLink(destination string) bool {
	parsed, err := url.Parse(destination)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https" || parsed.Scheme == "mailto")
}

func docxImageFormat(mimeType string) domain.ImageFormat {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0])) {
	case "image/png":
		return domain.ImageFormatPNG
	case "image/jpeg", "image/jpg":
		return domain.ImageFormatJPEG
	case "image/gif":
		return domain.ImageFormatGIF
	default:
		return ""
	}
}
