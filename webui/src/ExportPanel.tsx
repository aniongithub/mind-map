import { useState, useEffect } from 'preact/hooks';
import { api, ExportFormat } from './api';

interface ExportPanelProps {
    /** The current page path to export from */
    page: string;
    onClose: () => void;
}

/**
 * ExportPanel — triggered from the page header. Exports starting from the
 * current page, following wikilinks to the chosen depth.
 */
export function ExportPanel({ page, onClose }: ExportPanelProps) {
    const [formats, setFormats] = useState<ExportFormat[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);

    const [selectedFormat, setSelectedFormat] = useState('');
    const [depth, setDepth] = useState(0);

    useEffect(() => {
        api.exportFormats()
            .then((fmts) => {
                setFormats(fmts);
                if (fmts.length > 0) setSelectedFormat(fmts[0].name);
            })
            .catch((e) => setError(e.message))
            .finally(() => setLoading(false));
    }, []);

    const handleExport = () => {
        const url = api.exportUrl(selectedFormat, page, depth);
        const a = document.createElement('a');
        a.href = url;
        a.download = '';
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
    };

    const currentFormat = formats.find((f) => f.name === selectedFormat);

    if (loading) {
        return (
            <div class="export-panel">
                <div class="settings-section-title">Export</div>
                <div class="settings-field-help">Loading export formats…</div>
            </div>
        );
    }

    if (error) {
        return (
            <div class="export-panel">
                <div class="settings-section-title">Export</div>
                <div class="settings-reindex-error">Failed to load formats: {error}</div>
                <button class="btn" onClick={onClose}>Back</button>
            </div>
        );
    }

    if (formats.length === 0) {
        return (
            <div class="export-panel">
                <div class="settings-section-title">Export</div>
                <div class="settings-field-help">No export formats available.</div>
                <button class="btn" onClick={onClose}>Back</button>
            </div>
        );
    }

    return (
        <>
            <div class="settings-title">Export</div>
            <div class="settings-container">
                {/* Page info */}
                <div class="settings-section">
                    <div class="settings-section-title">Page</div>
                    <div class="settings-field">
                        <code>{page}</code>
                    </div>
                </div>

                {/* Depth */}
                <div class="settings-section">
                    <div class="settings-section-title">Depth</div>
                    <div class="settings-field-help">
                        How many wikilink hops to follow from this page.
                    </div>
                    <div class="export-depth-options">
                        <label class="export-format-option">
                            <input
                                type="radio"
                                name="export-depth"
                                checked={depth === 0}
                                onChange={() => setDepth(0)}
                            />
                            <span>Just this page</span>
                        </label>
                        <label class="export-format-option">
                            <input
                                type="radio"
                                name="export-depth"
                                checked={depth !== 0}
                                onChange={() => { if (depth === 0) setDepth(1); }}
                            />
                            <span>+ linked pages, with</span>
                            <input
                                type="number"
                                class="export-depth-input"
                                min="-1"
                                value={depth !== 0 ? depth : 1}
                                onInput={(e) => {
                                    const v = parseInt((e.target as HTMLInputElement).value, 10);
                                    if (!isNaN(v)) setDepth(v === 0 ? 1 : v);
                                }}
                                onFocus={() => { if (depth === 0) setDepth(1); }}
                            />
                            <span>hops (-1 for all)</span>
                        </label>
                    </div>
                </div>

                {/* Format selector */}
                <div class="settings-section">
                    <div class="settings-section-title">Format</div>
                    <div class="export-format-list">
                        {formats.map((fmt) => (
                            <label key={fmt.name} class="export-format-option">
                                <input
                                    type="radio"
                                    name="export-format"
                                    value={fmt.name}
                                    checked={selectedFormat === fmt.name}
                                    onChange={() => setSelectedFormat(fmt.name)}
                                />
                                <div class="export-format-info">
                                    <span class="export-format-name">{fmt.name}</span>
                                    <span class="export-format-desc">{fmt.description}</span>
                                </div>
                            </label>
                        ))}
                    </div>
                </div>

                {/* Actions */}
                <div class="settings-actions">
                    <button class="btn primary" onClick={handleExport}>
                        Export{currentFormat ? ` as ${currentFormat.extension}` : ''}
                    </button>
                    <button class="btn" onClick={onClose}>
                        Back
                    </button>
                </div>
            </div>
        </>
    );
}
