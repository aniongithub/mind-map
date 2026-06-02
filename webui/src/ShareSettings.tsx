import { useState, useEffect } from 'preact/hooks';
import { api, ExportFormat, ExportSettingsField } from './api';

/**
 * ShareSettings renders per-plugin settings for all registered export
 * formats in the main Settings panel. Each plugin gets a collapsible
 * section with its fields rendered generically from the schema —
 * like VS Code extension settings.
 */
export function ShareSettings() {
    const [formats, setFormats] = useState<ExportFormat[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);
    const [values, setValues] = useState<Record<string, Record<string, any>>>({});
    const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});

    useEffect(() => {
        api.exportFormats()
            .then((fmts) => {
                setFormats(fmts);
                // Initialize with defaults
                const defaults: Record<string, Record<string, any>> = {};
                for (const fmt of fmts) {
                    defaults[fmt.name] = {};
                    for (const field of fmt.settings.fields) {
                        defaults[fmt.name][field.key] = field.default;
                    }
                }
                setValues(defaults);
            })
            .catch((e) => setError(e.message))
            .finally(() => setLoading(false));
    }, []);

    const updateValue = (format: string, key: string, value: any) => {
        setValues((prev) => ({
            ...prev,
            [format]: { ...(prev[format] || {}), [key]: value },
        }));
    };

    const toggleCollapse = (name: string) => {
        setCollapsed((prev) => ({ ...prev, [name]: !prev[name] }));
    };

    if (loading) {
        return (
            <div class="settings-section">
                <div class="settings-section-title">Export Extensions</div>
                <div class="settings-field-help">Loading…</div>
            </div>
        );
    }

    if (error) {
        return (
            <div class="settings-section">
                <div class="settings-section-title">Export Extensions</div>
                <div class="settings-reindex-error">{error}</div>
            </div>
        );
    }

    if (formats.length === 0) return null;

    return (
        <div class="settings-section">
            <div class="settings-section-title">Export Extensions</div>
            <div class="settings-field-help">
                Settings for registered export formats. These are the defaults used when exporting pages.
            </div>
            {formats.map((fmt) => (
                <div class="share-extension" key={fmt.name}>
                    <button
                        class="share-extension-header"
                        onClick={() => toggleCollapse(fmt.name)}
                        type="button"
                    >
                        <span class="share-extension-chevron">
                            {collapsed[fmt.name] ? '▸' : '▾'}
                        </span>
                        <span class="share-extension-name">{fmt.name}</span>
                        <span class="share-extension-desc">{fmt.description}</span>
                    </button>
                    {!collapsed[fmt.name] && (
                        <div class="share-extension-fields">
                            {fmt.settings.fields.length === 0 ? (
                                <div class="settings-field-help">No configurable settings.</div>
                            ) : (
                                fmt.settings.fields.map((field) => (
                                    <SettingsFieldRenderer
                                        key={field.key}
                                        field={field}
                                        value={values[fmt.name]?.[field.key]}
                                        onChange={(v) => updateValue(fmt.name, field.key, v)}
                                    />
                                ))
                            )}
                        </div>
                    )}
                </div>
            ))}
        </div>
    );
}

/** Generic renderer for a single settings field based on its schema type. */
function SettingsFieldRenderer({
    field,
    value,
    onChange,
}: {
    field: ExportSettingsField;
    value: any;
    onChange: (v: any) => void;
}) {
    switch (field.type) {
        case 'bool':
            return (
                <div class="settings-field">
                    <div class="settings-field-toggle">
                        <input
                            type="checkbox"
                            id={`share-${field.key}`}
                            checked={value ?? field.default}
                            onChange={(e) => onChange((e.target as HTMLInputElement).checked)}
                        />
                        <label for={`share-${field.key}`}>{field.label}</label>
                    </div>
                    {field.description && <div class="hint">{field.description}</div>}
                </div>
            );
        case 'enum':
            return (
                <div class="settings-field">
                    <label>{field.label}</label>
                    {field.description && <div class="hint">{field.description}</div>}
                    <select
                        value={value ?? field.default}
                        onChange={(e) => onChange((e.target as HTMLSelectElement).value)}
                    >
                        {field.enum?.map((opt) => (
                            <option key={opt} value={opt}>{opt}</option>
                        ))}
                    </select>
                </div>
            );
        case 'int':
            return (
                <div class="settings-field">
                    <label>{field.label}</label>
                    {field.description && <div class="hint">{field.description}</div>}
                    <input
                        type="number"
                        value={value ?? field.default}
                        onInput={(e) => onChange(parseInt((e.target as HTMLInputElement).value, 10) || 0)}
                    />
                </div>
            );
        default:
            return (
                <div class="settings-field">
                    <label>{field.label}</label>
                    {field.description && <div class="hint">{field.description}</div>}
                    <input
                        type="text"
                        value={value ?? field.default ?? ''}
                        onInput={(e) => onChange((e.target as HTMLInputElement).value)}
                    />
                </div>
            );
    }
}
