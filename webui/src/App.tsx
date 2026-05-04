import { useState, useEffect, useRef } from 'preact/hooks';
import { mcp, Page } from './mcp';
import { marked } from 'marked';
import mermaid from 'mermaid';

mermaid.initialize({ startOnLoad: false, theme: 'default' });

let mermaidCounter = 0;

interface SyncSettings {
    enabled: boolean;
    remote: string;
    interval: string;
    token: string;
}

interface Settings {
    sync: SyncSettings;
}

async function loadSettings(): Promise<Settings> {
    const res = await fetch('/api/settings');
    return res.json();
}

async function saveSettings(s: Settings): Promise<Settings> {
    const res = await fetch('/api/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(s),
    });
    return res.json();
}

async function getConfigPath(): Promise<string> {
    const res = await fetch('/api/settings/path');
    const data = await res.json();
    return data.path;
}

async function requestRestart(): Promise<void> {
    await fetch('/api/restart', { method: 'POST' });
}

export function App() {
    const [pages, setPages] = useState<Page[]>([]);
    const [current, setCurrent] = useState<Page | null>(null);
    const [editing, setEditing] = useState(false);
    const [editContent, setEditContent] = useState('');
    const [searchQuery, setSearchQuery] = useState('');
    const [showSettings, setShowSettings] = useState(false);
    const [settings, setSettings] = useState<Settings | null>(null);
    const [configPath, setConfigPath] = useState('');
    const [settingsDirty, setSettingsDirty] = useState(false);
    const [settingsSaved, setSettingsSaved] = useState(false);
    const [isDark, setIsDark] = useState(() => {
        const saved = localStorage.getItem('mm-theme');
        if (saved) return saved === 'dark';
        return window.matchMedia('(prefers-color-scheme: dark)').matches;
    });

    useEffect(() => {
        document.documentElement.classList.toggle('dark', isDark);
        localStorage.setItem('mm-theme', isDark ? 'dark' : 'light');
    }, [isDark]);

    // Load page list
    const loadPages = async () => {
        try {
            const list = await mcp.listPages();
            setPages(list);
        } catch (e) {
            console.error('Failed to load pages:', e);
        }
    };

    useEffect(() => { loadPages(); }, []);

    // Hash routing
    const getHashPath = (): string | null => {
        const hash = window.location.hash.replace(/^#\/?/, '');
        return hash || null;
    };

    useEffect(() => {
        const onHash = () => {
            const path = getHashPath();
            if (path) openPage(path);
            else setCurrent(null);
        };
        window.addEventListener('hashchange', onHash);
        // Load initial page from hash
        const initial = getHashPath();
        if (initial) openPage(initial);
        return () => window.removeEventListener('hashchange', onHash);
    }, []);

    const navigate = (path: string | null) => {
        window.location.hash = path ? `/${path}` : '/';
    };

    const openPage = async (path: string) => {
        try {
            const page = await mcp.getPage(path);
            setCurrent(page);
            setEditing(false);
            setShowSettings(false);
        } catch (e) {
            console.error('Failed to open page:', e);
        }
    };

    const handleSave = async () => {
        if (!current) return;
        try {
            await mcp.updatePage(current.path, editContent);
            await openPage(current.path);
            await loadPages();
        } catch (e) {
            console.error('Failed to save:', e);
        }
    };

    const handleEdit = () => {
        if (!current) return;
        // Reconstruct full content with frontmatter
        let content = '';
        if (current.frontmatter && Object.keys(current.frontmatter).length > 0) {
            content += '---\n';
            for (const [k, v] of Object.entries(current.frontmatter)) {
                content += `${k}: ${v}\n`;
            }
            content += '---\n';
        }
        content += current.body;
        setEditContent(content);
        setEditing(true);
    };

    const handleSearch = async () => {
        if (!searchQuery.trim()) {
            loadPages();
            return;
        }
        try {
            const results = await mcp.searchPages(searchQuery);
            setPages(results.map(r => ({ path: r.path, title: r.title, body: '', modified_at: '' })));
        } catch (e) {
            console.error('Search failed:', e);
        }
    };

    const openSettings = async () => {
        try {
            const [s, p] = await Promise.all([loadSettings(), getConfigPath()]);
            setSettings(s);
            setConfigPath(p);
            setShowSettings(true);
            setSettingsDirty(false);
            setSettingsSaved(false);
            setCurrent(null);
        } catch (e) {
            console.error('Failed to load settings:', e);
        }
    };

    const handleSettingsSave = async () => {
        if (!settings) return;
        try {
            const saved = await saveSettings(settings);
            setSettings(saved);
            setSettingsDirty(false);
            setSettingsSaved(true);
        } catch (e) {
            console.error('Failed to save settings:', e);
        }
    };

    const handleRestart = async () => {
        try {
            await requestRestart();
            // Wait then reload
            setTimeout(() => window.location.reload(), 2000);
        } catch (e) {
            console.error('Restart failed:', e);
        }
    };

    const updateSync = (field: keyof SyncSettings, value: string | boolean) => {
        if (!settings) return;
        setSettings({
            ...settings,
            sync: { ...settings.sync, [field]: value },
        });
        setSettingsDirty(true);
        setSettingsSaved(false);
    };

    const renderMarkdown = (body: string): string => {
        // Convert [[wikilinks]] to clickable links before rendering
        const withLinks = body.replace(/\[\[([^\]|]+)(?:\|([^\]]+))?\]\]/g, (_, target, display) => {
            const label = display || target;
            return `[${label}](#/${target})`;
        });

        // Extract mermaid blocks before marked processing to prevent HTML escaping
        const mermaidBlocks: Record<string, string> = {};
        const withPlaceholders = withLinks.replace(/```mermaid\s*\n([\s\S]*?)```/g, (_, code) => {
            const id = `mermaid-${++mermaidCounter}`;
            mermaidBlocks[id] = code.trim();
            return `<div class="mermaid" id="${id}">MERMAID_PLACEHOLDER_${id}</div>`;
        });

        let html = marked.parse(withPlaceholders, { async: false }) as string;

        // Re-inject raw mermaid code after marked processing
        for (const [id, code] of Object.entries(mermaidBlocks)) {
            html = html.replace(`MERMAID_PLACEHOLDER_${id}`, code);
        }

        return html;
    };

    const bodyRef = useRef<HTMLDivElement>(null);

    // Render mermaid diagrams after DOM update
    useEffect(() => {
        if (bodyRef.current && !editing) {
            const els = bodyRef.current.querySelectorAll('.mermaid');
            if (els.length > 0) {
                // Update mermaid theme to match current mode
                mermaid.initialize({
                    startOnLoad: false,
                    theme: isDark ? 'dark' : 'default',
                });
                mermaid.run({ nodes: els as unknown as ArrayLike<HTMLElement> });
            }
        }
    }, [current, editing, isDark]);

    const pageCount = pages.length;

    return (
        <div class="app">
            {/* Sidebar */}
            <div class="sidebar">
                <div class="sidebar-header">mind-map</div>
                <div class="sidebar-search">
                    <input
                        type="text"
                        placeholder="search..."
                        value={searchQuery}
                        onInput={(e) => setSearchQuery((e.target as HTMLInputElement).value)}
                        onKeyDown={(e) => { if (e.key === 'Enter') handleSearch(); }}
                    />
                </div>
                <ul class="page-list">
                    {pages.map(p => (
                        <li
                            key={p.path}
                            class={`page-item ${current?.path === p.path ? 'active' : ''}`}
                            onClick={() => navigate(p.path)}
                        >
                            <div class="page-item-title">{p.title || p.path}</div>
                            <div class="page-item-path">{p.path}</div>
                        </li>
                    ))}
                </ul>
                <div class="status-bar">
                    <span>{pageCount} pages</span>
                    <div class="status-bar-left">
                        <button class="settings-toggle" onClick={openSettings} title="Settings">
                            &#9881;
                        </button>
                        <button class="theme-toggle" onClick={() => setIsDark(!isDark)}>
                            {isDark ? '\u2600' : '\u263E'}
                        </button>
                    </div>
                </div>
            </div>

            {/* Main */}
            <div class="main">
                {showSettings && settings ? (
                    <>
                        <div class="settings-title">Settings</div>
                        <div class="settings-container">
                            {settingsSaved && (
                                <div class="settings-banner">
                                    <span>Settings saved. Restart to apply.</span>
                                    <button class="btn primary" onClick={handleRestart}>Restart now</button>
                                </div>
                            )}

                            <div class="settings-section">
                                <div class="settings-section-title">Wiki Sync</div>

                                <div class="settings-field">
                                    <div class="settings-field-toggle">
                                        <input
                                            type="checkbox"
                                            id="sync-enabled"
                                            checked={settings.sync.enabled}
                                            onChange={(e) => updateSync('enabled', (e.target as HTMLInputElement).checked)}
                                        />
                                        <label for="sync-enabled">Enable sync</label>
                                    </div>
                                </div>

                                <div class="settings-field">
                                    <label>Remote URL</label>
                                    <div class="hint">Git remote for sync (e.g. https://github.com/user/repo.wiki.git)</div>
                                    <input
                                        type="text"
                                        value={settings.sync.remote}
                                        onInput={(e) => updateSync('remote', (e.target as HTMLInputElement).value)}
                                        placeholder="https://github.com/user/repo.wiki.git"
                                    />
                                </div>

                                <div class="settings-field">
                                    <label>Sync Interval</label>
                                    <div class="hint">How often to pull and push (e.g. 30s, 1m, 5m)</div>
                                    <input
                                        type="text"
                                        value={settings.sync.interval}
                                        onInput={(e) => updateSync('interval', (e.target as HTMLInputElement).value)}
                                        placeholder="30s"
                                    />
                                </div>

                                <div class="settings-field">
                                    <label>Auth Token</label>
                                    <div class="hint">Personal access token for private repos</div>
                                    <input
                                        type="password"
                                        value={settings.sync.token}
                                        onInput={(e) => updateSync('token', (e.target as HTMLInputElement).value)}
                                        placeholder="ghp_..."
                                    />
                                </div>
                            </div>

                            <div class="settings-actions">
                                <button class="btn primary" onClick={handleSettingsSave} disabled={!settingsDirty}>
                                    Save
                                </button>
                                <button class="btn" onClick={() => setShowSettings(false)}>
                                    Back
                                </button>
                            </div>

                            <div class="settings-reset">
                                To restore defaults, delete <code>{configPath}</code> and restart.
                            </div>
                        </div>
                    </>
                ) : current ? (
                    <>
                        <div class="page-header">
                            <div class="page-title">{current.title}</div>
                            <div class="page-meta">
                                <span>{current.path}</span>
                                {current.modified_at && <span>{new Date(current.modified_at).toLocaleDateString()}</span>}
                                {current.links && current.links.length > 0 && (
                                    <span>{current.links.length} links</span>
                                )}
                            </div>
                            <div class="page-actions">
                                {editing ? (
                                    <>
                                        <button class="btn primary" onClick={handleSave}>save</button>
                                        <button class="btn" onClick={() => setEditing(false)}>cancel</button>
                                    </>
                                ) : (
                                    <button class="btn" onClick={handleEdit}>edit</button>
                                )}
                            </div>
                        </div>

                        {editing ? (
                            <div class="editor-container">
                                <textarea
                                    class="editor-textarea"
                                    value={editContent}
                                    onInput={(e) => setEditContent((e.target as HTMLTextAreaElement).value)}
                                />
                            </div>
                        ) : (
                            <>
                                <div class="page-body" ref={bodyRef}>
                                    <div
                                        class="markdown"
                                        dangerouslySetInnerHTML={{ __html: renderMarkdown(current.body) }}
                                    />
                                </div>
                                {current.backlinks && current.backlinks.length > 0 && (
                                    <div class="backlinks">
                                        <div class="backlinks-title">Linked from</div>
                                        {current.backlinks.map(bl => (
                                            <div key={bl} class="backlink-item" onClick={() => navigate(bl)}>
                                                {bl}
                                            </div>
                                        ))}
                                    </div>
                                )}
                            </>
                        )}
                    </>
                ) : (
                    <div class="empty">select a page</div>
                )}
            </div>
        </div>
    );
}
