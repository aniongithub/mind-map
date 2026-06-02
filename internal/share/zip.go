package share

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// ZipSharer exports wiki pages as a zip archive of markdown files with
// optional asset inclusion.
type ZipSharer struct{}

func init() {
	Register(&ZipSharer{})
}

func (z *ZipSharer) Name() string        { return "zip" }
func (z *ZipSharer) Description() string  { return "Zip archive of markdown files with optional assets" }
func (z *ZipSharer) ContentType() string  { return "application/zip" }
func (z *ZipSharer) FileExtension() string { return ".zip" }

func (z *ZipSharer) Settings() SharerSettings {
	return SharerSettings{
		Fields: []SettingsField{
			{
				Key:         "include_assets",
				Label:       "Include images/assets",
				Description: "Bundle referenced images from .assets/ directories into the zip",
				Type:        "bool",
				Default:     true,
			},
			{
				Key:         "flatten",
				Label:       "Flatten directory structure",
				Description: "Place all files in the zip root with path separators replaced by dashes",
				Type:        "bool",
				Default:     false,
			},
		},
	}
}

func (z *ZipSharer) Export(ctx context.Context, w io.Writer, req ExportRequest) error {
	includeAssets := SettingBool(req.Config, "include_assets", true)
	flatten := SettingBool(req.Config, "flatten", false)

	zw := zip.NewWriter(w)
	defer zw.Close()

	for _, page := range req.Pages {
		if err := ctx.Err(); err != nil {
			return err
		}

		mdContent := reconstitute(page)
		mdPath := pageMdPath(page.Path, flatten)

		hdr := &zip.FileHeader{
			Name:     mdPath,
			Method:   zip.Deflate,
			Modified: page.ModifiedAt,
		}
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return fmt.Errorf("create zip entry %s: %w", mdPath, err)
		}
		if _, err := io.WriteString(fw, mdContent); err != nil {
			return fmt.Errorf("write zip entry %s: %w", mdPath, err)
		}

		if includeAssets && req.Assets != nil {
			for _, assetPath := range page.ImageRefs {
				if err := ctx.Err(); err != nil {
					return err
				}
				content, _, err := req.Assets.ReadAsset(ctx, assetPath)
				if err != nil {
					continue // skip missing assets
				}
				zipAssetPath := assetZipPath(assetPath, flatten)
				ahdr := &zip.FileHeader{
					Name:     zipAssetPath,
					Method:   zip.Deflate,
					Modified: page.ModifiedAt,
				}
				afw, err := zw.CreateHeader(ahdr)
				if err != nil {
					return fmt.Errorf("create asset entry %s: %w", zipAssetPath, err)
				}
				if _, err := afw.Write(content); err != nil {
					return fmt.Errorf("write asset entry %s: %w", zipAssetPath, err)
				}
			}
		}
	}

	return nil
}

// reconstitute rebuilds a markdown file from a Page, including frontmatter.
func reconstitute(p Page) string {
	var sb strings.Builder
	if len(p.Frontmatter) > 0 {
		sb.WriteString("---\n")
		for k, v := range p.Frontmatter {
			sb.WriteString(fmt.Sprintf("%s: %v\n", k, yamlValue(v)))
		}
		sb.WriteString("---\n\n")
	}
	sb.WriteString(p.Body)
	if !strings.HasSuffix(p.Body, "\n") {
		sb.WriteString("\n")
	}
	return sb.String()
}

// yamlValue provides a simple YAML-safe string representation of a value.
func yamlValue(v any) string {
	switch val := v.(type) {
	case string:
		if strings.ContainsAny(val, ":#{}[]&*!|>'\"%@`") || val == "" {
			return fmt.Sprintf("%q", val)
		}
		return val
	case []interface{}:
		parts := make([]string, len(val))
		for i, item := range val {
			parts[i] = fmt.Sprintf("%v", item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case time.Time:
		return val.Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// pageMdPath computes the zip-internal path for a markdown file.
func pageMdPath(pagePath string, flatten bool) string {
	if flatten {
		return strings.ReplaceAll(pagePath, "/", "--") + ".md"
	}
	return pagePath + ".md"
}

// assetZipPath computes the zip-internal path for an asset file.
func assetZipPath(assetPath string, flatten bool) string {
	if flatten {
		return strings.ReplaceAll(assetPath, "/", "--")
	}
	return assetPath
}
