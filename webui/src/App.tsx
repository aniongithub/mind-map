import { useState, useEffect, useRef } from 'preact/hooks';
import { api, Page } from './mcp';
import { marked } from 'marked';
import mermaid from 'mermaid';

mermaid.initialize({ startOnLoad: false, theme: 'default' });

interface SyncSettings {
    enabled: boolean;
    default: string;
    interval: string;
    mappings?: { prefix: string; remote: string }[];
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

    // Sidebar resize/collapse state
    const [sidebarWidth, setSidebarWidth] = useState(() => {
        const saved = localStorage.getItem('mm-sidebar-width');
        return saved ? parseInt(saved, 10) : 240;
    });
    const [sidebarCollapsed, setSidebarCollapsed] = useState(() => {
        return localStorage.getItem('mm-sidebar-collapsed') === 'true';
    });
    const isResizing = useRef(false);

    useEffect(() => {
        localStorage.setItem('mm-sidebar-width', String(sidebarWidth));
    }, [sidebarWidth]);

    useEffect(() => {
        localStorage.setItem('mm-sidebar-collapsed', String(sidebarCollapsed));
    }, [sidebarCollapsed]);

    const startResize = (e: MouseEvent) => {
        e.preventDefault();
        isResizing.current = true;
        document.body.style.cursor = 'col-resize';
        document.body.style.userSelect = 'none';

        const onMouseMove = (ev: MouseEvent) => {
            if (!isResizing.current) return;
            const newWidth = Math.max(160, Math.min(480, ev.clientX));
            setSidebarWidth(newWidth);
        };
        const onMouseUp = () => {
            isResizing.current = false;
            document.body.style.cursor = '';
            document.body.style.userSelect = '';
            document.removeEventListener('mousemove', onMouseMove);
            document.removeEventListener('mouseup', onMouseUp);
        };
        document.addEventListener('mousemove', onMouseMove);
        document.addEventListener('mouseup', onMouseUp);
    };

    // Backlinks collapse state
    const [backlinksCollapsed, setBacklinksCollapsed] = useState(() => {
        return localStorage.getItem('mm-backlinks-collapsed') === 'true';
    });

    useEffect(() => {
        localStorage.setItem('mm-backlinks-collapsed', String(backlinksCollapsed));
    }, [backlinksCollapsed]);

    // Sort mode: 'recent' | 'path' | 'title'
    type SortMode = 'recent' | 'path' | 'title';
    const sortModes: SortMode[] = ['recent', 'path', 'title'];
    const sortLabels: Record<SortMode, string> = { recent: 'Recent', path: 'A→Z path', title: 'A→Z title' };

    const SortIcon = ({ mode }: { mode: SortMode }) => {
        const props = { width: 16, height: 16, fill: 'currentColor', viewBox: '' as string };
        switch (mode) {
            case 'recent':
                return <svg {...props} viewBox="-5 -10 110 110"><path d="m54.871 6.9883c-1.5664 0-3.1367 0.066407-4.707 0.18359-4.1836 0.39453-8.3438 1.418-12.355 3.082-13.742 5.6992-23.395 18.035-25.883 32.383l-2.543-2.8828-2.0234-2.2969-4.5781 4.0508 2.0117 2.2969 9.1406 10.344 11.043-8.3711 2.4375-1.8477-3.6875-4.8711-2.4375 1.8438-3.2891 2.4961c2.2109-12.195 10.449-22.637 22.156-27.492 13.777-5.7148 29.605-2.5625 40.152 7.9961 10.543 10.562 13.688 26.43 7.9805 40.227-5.707 13.801-19.125 22.777-34.035 22.777h-3.0586v6.1133h3.0586c17.371 0 33.055-10.492 39.703-26.559 6.6445-16.066 2.9688-34.582-9.3125-46.883-8.0586-8.0703-18.805-12.43-29.77-12.594zm-0.61328 19.598c-12.879 0-23.383 10.531-23.383 23.426s10.504 23.41 23.383 23.41c12.879 0 23.383-10.516 23.383-23.41s-10.504-23.426-23.383-23.426zm-1.7266 10.449h6.1133v11.184l2.9141 4.1914 1.7422 2.5156-5.0312 3.4805-1.7422-2.5156-3.9961-5.7656z"/></svg>;
            case 'path':
                return <svg {...props} viewBox="26 -26 100 100"><path fill-rule="evenodd" clip-rule="evenodd" d="M114.9,5.7c-1.4,1.5-3.7,1.5-5.1,0L100.5-4v64.5c0,2-1.6,3.6-3.6,3.6s-3.6-1.6-3.6-3.6V-4l-9.3,9.7  c-1.4,1.5-3.7,1.5-5.1,0c-1.4-1.5-1.4-3.9,0-5.4l15.1-15.9c0,0,0,0,0.1-0.1l0.2-0.2c0,0,0,0,0,0c0.6-0.7,1.5-1.1,2.5-1.1  c1,0,1.9,0.4,2.6,1.1c0,0,0,0,0,0c0,0,0,0,0,0c0,0,0,0,0,0l15.4,16.2C116.3,1.8,116.3,4.2,114.9,5.7z M56.9,62.6  C56.9,62.6,56.9,62.6,56.9,62.6l-0.3,0.3c0,0,0,0,0,0c-0.6,0.7-1.5,1.1-2.5,1.1c-1,0-1.9-0.4-2.6-1.1c0,0,0,0,0,0c0,0,0,0,0,0  c0,0,0,0,0,0L36.1,46.7c-1.4-1.5-1.4-3.9,0-5.4c1.4-1.5,3.7-1.5,5.1,0l9.3,9.7v-64.5c0-2,1.6-3.6,3.6-3.6c2,0,3.6,1.6,3.6,3.6V51  l9.3-9.7c1.4-1.5,3.7-1.5,5.1,0c1.4,1.5,1.4,3.9,0,5.4L56.9,62.6z"/></svg>;
            case 'title':
                return <svg {...props} viewBox="0 0 100 96"><path d="M26.672,17.764C26.14,16.116,24.604,15,22.868,15c-1.732,0-3.264,1.116-3.8,2.756l-18.872,58  c-0.68,2.096,0.468,4.36,2.572,5.049C3.18,80.936,3.596,81,4.008,81c1.684,0,3.252-1.072,3.804-2.756L12.12,65h21.508l4.304,13.236  c0.684,2.1,2.932,3.252,5.04,2.576c2.096-0.681,3.248-2.937,2.568-5.045L26.672,17.764z M14.712,57l8.156-25.072L31.02,57H14.712z"/><path d="M95.8,73H64.973L99.1,23.264c0.84-1.232,0.928-2.82,0.24-4.132S97.284,17,95.8,17h-32c-2.208,0-4,1.788-4,4s1.792,4,4,4  h24.408L54.08,74.736c-0.84,1.231-0.932,2.812-0.248,4.123C54.527,80.18,55.893,81,57.376,81H95.8c2.212,0,4-1.788,4-4  S98,73,95.8,73L95.8,73z"/><path d="M58.584,55.624l8-7.756c0.768-0.752,1.216-1.792,1.216-2.88c0-1.084-0.436-2.112-1.216-2.876l-8-7.752  c-1.588-1.54-4.12-1.5-5.656,0.088c-1.539,1.584-1.496,4.112,0.084,5.656l0.916,0.88H43.804c-2.208,0-4,1.788-4,4s1.792,4,4,4  h10.124l-0.916,0.875c-1.584,1.541-1.623,4.072-0.084,5.66c0.78,0.813,1.828,1.225,2.872,1.225  C56.8,56.752,57.809,56.376,58.584,55.624z"/></svg>;
        }
    };

    const [sortMode, setSortMode] = useState<SortMode>(() => {
        const saved = localStorage.getItem('mm-sort-mode');
        return (saved === 'path' || saved === 'title') ? saved : 'recent';
    });

    useEffect(() => {
        localStorage.setItem('mm-sort-mode', sortMode);
    }, [sortMode]);

    const cycleSortMode = () => {
        const idx = sortModes.indexOf(sortMode);
        setSortMode(sortModes[(idx + 1) % sortModes.length]);
    };

    const sortPages = (list: Page[]): Page[] => {
        const sorted = [...list];
        switch (sortMode) {
            case 'path':
                sorted.sort((a, b) => a.path.localeCompare(b.path));
                break;
            case 'title':
                sorted.sort((a, b) => (a.title || a.path).localeCompare(b.title || b.path));
                break;
            case 'recent':
            default:
                // API already returns modified DESC; preserve that order
                break;
        }
        return sorted;
    };

    useEffect(() => {
        document.documentElement.classList.toggle('dark', isDark);
        localStorage.setItem('mm-theme', isDark ? 'dark' : 'light');
    }, [isDark]);

    // Load page list
    const [rawPages, setRawPages] = useState<Page[]>([]);

    const loadPages = async () => {
        try {
            const list = await api.listPages();
            setRawPages(list);
        } catch (e) {
            console.error('Failed to load pages:', e);
        }
    };

    // Re-sort whenever rawPages or sortMode changes
    useEffect(() => {
        setPages(sortPages(rawPages));
    }, [rawPages, sortMode]);

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
            const page = await api.getPage(path);
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
            await api.updatePage(current.path, editContent);
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
            const results = await api.searchPages(searchQuery);
            setRawPages(results.map(r => ({ path: r.path, title: r.title, body: '', modified_at: '' })));
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
        let localCounter = 0;
        const withPlaceholders = withLinks.replace(/```mermaid\s*\n([\s\S]*?)```/g, (_, code) => {
            const id = `mermaid-${++localCounter}`;
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
            <div
                class={`sidebar ${sidebarCollapsed ? 'collapsed' : ''}`}
                style={sidebarCollapsed ? undefined : { width: `${sidebarWidth}px` }}
            >
                <div class="sidebar-header">
                    <span class="sidebar-header-text">mind-map</span>
                    <button
                        class="sidebar-collapse-btn"
                        onClick={() => setSidebarCollapsed(!sidebarCollapsed)}
                        title={sidebarCollapsed ? 'Expand sidebar' : 'Collapse sidebar'}
                    >
                        {sidebarCollapsed ? '\u25B6' : '\u25C0'}
                    </button>
                </div>
                {!sidebarCollapsed && (
                    <>
                        <div class="sidebar-search">
                            <div class="search-wrapper">
                                <input
                                    type="text"
                                    placeholder="search..."
                                    value={searchQuery}
                                    onInput={(e) => setSearchQuery((e.target as HTMLInputElement).value)}
                                    onKeyDown={(e) => { if (e.key === 'Enter') handleSearch(); }}
                                />
                                <button
                                    class="sort-toggle"
                                    onClick={cycleSortMode}
                                    title={`Sort: ${sortLabels[sortMode]}`}
                                >
                                    <SortIcon mode={sortMode} />
                                </button>
                            </div>
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
                    </>
                )}
                {!sidebarCollapsed && (
                    <div class="sidebar-resize-handle" onMouseDown={startResize} />
                )}
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
                                    <label>Default Remote</label>
                                    <div class="hint">Catch-all git remote for pages without a specific mapping</div>
                                    <input
                                        type="text"
                                        value={settings.sync.default}
                                        onInput={(e) => updateSync('default', (e.target as HTMLInputElement).value)}
                                        placeholder="https://github.com/user/wiki.wiki.git"
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

                                {settings.sync.mappings && settings.sync.mappings.length > 0 && (
                                    <div class="settings-field">
                                        <label>Active Mappings</label>
                                        <div class="hint">Registered by agents via register_sync</div>
                                        <div class="settings-mappings">
                                            {settings.sync.mappings.map(m => (
                                                <div key={m.prefix} class="settings-mapping">
                                                    <code>{m.prefix}</code> &rarr; <code>{m.remote}</code>
                                                </div>
                                            ))}
                                        </div>
                                    </div>
                                )}
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
                                <div class="page-body" ref={bodyRef} key={`${current.path}-${current.modified_at}`}>
                                    <div
                                        class="markdown"
                                        dangerouslySetInnerHTML={{ __html: renderMarkdown(current.body) }}
                                    />
                                </div>
                                {current.backlinks && current.backlinks.length > 0 && (
                                    <div class="backlinks">
                                        <div
                                            class="backlinks-title"
                                            onClick={() => setBacklinksCollapsed(!backlinksCollapsed)}
                                        >
                                            <span class="backlinks-toggle">{backlinksCollapsed ? '\u25B6' : '\u25BC'}</span>
                                            Linked from ({current.backlinks.length})
                                        </div>
                                        {!backlinksCollapsed && current.backlinks.map(bl => (
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
