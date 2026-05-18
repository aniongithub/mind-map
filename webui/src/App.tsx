import { useState, useEffect, useRef, useMemo } from 'preact/hooks';
import { api, Page } from './mcp';
import { Logo } from './Logo';
import { PageBrowser } from './PageBrowser';
import { searchTokens, searchRegex, Highlighted } from './search';
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
    const [current, setCurrent] = useState<Page | null>(null);
    const [editing, setEditing] = useState(false);
    const [editContent, setEditContent] = useState('');
    const [searchQuery, setSearchQuery] = useState(() => localStorage.getItem('mm-search-query') || '');
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

    useEffect(() => {
        document.documentElement.classList.toggle('dark', isDark);
        localStorage.setItem('mm-theme', isDark ? 'dark' : 'light');
    }, [isDark]);

    // Load page list (raw, in API order). PageBrowser handles sorting.
    const [rawPages, setRawPages] = useState<Page[]>([]);

    const loadPages = async () => {
        try {
            const list = await api.listPages();
            setRawPages(list);
        } catch (e) {
            console.error('Failed to load pages:', e);
        }
    };

    // Persist search query so it survives reload; on first mount restore
    // either the filtered list (if a query was saved) or the full page list.
    useEffect(() => {
        localStorage.setItem('mm-search-query', searchQuery);
    }, [searchQuery]);

    useEffect(() => {
        if (searchQuery.trim()) {
            handleSearch();
        } else {
            loadPages();
        }
    }, []);

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

    // Wrap each occurrence of any search token in <mark>. Works on parsed
    // DOM (not via regex on raw HTML) so tags and attributes are never
    // touched. Skips <script>/<style>/<mark> to avoid breaking embedded
    // content or double-wrapping.
    const highlightHTML = (html: string, query: string): string => {
        const re = searchRegex(searchTokens(query));
        if (!re) return html;

        const doc = new DOMParser().parseFromString(`<div>${html}</div>`, 'text/html');
        const root = doc.body.firstElementChild as HTMLElement;
        const skip = new Set(['SCRIPT', 'STYLE', 'MARK']);
        const walker = doc.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
            acceptNode(n) {
                if (!n.parentElement) return NodeFilter.FILTER_REJECT;
                if (skip.has(n.parentElement.tagName)) return NodeFilter.FILTER_REJECT;
                return NodeFilter.FILTER_ACCEPT;
            },
        } as NodeFilter);
        const targets: Text[] = [];
        let t: Node | null;
        while ((t = walker.nextNode())) targets.push(t as Text);

        for (const node of targets) {
            const text = node.nodeValue || '';
            re.lastIndex = 0;
            if (!re.test(text)) continue;
            re.lastIndex = 0;
            const frag = doc.createDocumentFragment();
            let last = 0;
            let m: RegExpExecArray | null;
            while ((m = re.exec(text)) !== null) {
                if (m.index > last) frag.appendChild(doc.createTextNode(text.slice(last, m.index)));
                const mark = doc.createElement('mark');
                mark.textContent = m[0];
                frag.appendChild(mark);
                last = m.index + m[0].length;
            }
            if (last < text.length) frag.appendChild(doc.createTextNode(text.slice(last)));
            node.parentNode!.replaceChild(frag, node);
        }
        return root.innerHTML;
    };

    const renderedBodyHTML = useMemo(() => {
        if (!current) return '';
        const html = renderMarkdown(current.body);
        return editing ? html : highlightHTML(html, searchQuery);
    }, [current?.body, searchQuery, editing]);

    const bodyRef = useRef<HTMLDivElement>(null);

    // Render mermaid diagrams after DOM update. renderedBodyHTML is in
    // the deps because Preact replaces the .mermaid <div>s whenever the
    // rendered body changes (notably when the search filter changes and
    // the body gets re-highlighted) — the new nodes need to be processed.
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
    }, [renderedBodyHTML, editing, isDark]);

    // After the rendered body is in the DOM, scroll the first highlighted
    // match into view so the user doesn't have to hunt through long pages.
    useEffect(() => {
        if (editing || !bodyRef.current || !searchQuery.trim()) return;
        const first = bodyRef.current.querySelector('mark');
        if (first) {
            first.scrollIntoView({ block: 'center', behavior: 'smooth' });
        }
    }, [renderedBodyHTML]);

    const pageCount = rawPages.length;

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
                        <PageBrowser
                            pages={rawPages}
                            searchQuery={searchQuery}
                            onSearchChange={setSearchQuery}
                            onSearchSubmit={handleSearch}
                            onSearchClear={() => { setSearchQuery(''); loadPages(); }}
                            currentPath={current?.path}
                            onNavigate={navigate}
                        />
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
                            <span class="page-header-logo" aria-hidden="true"><Logo size={72} /></span>
                            <div class="page-title">
                                <Highlighted text={current.title} query={searchQuery} />
                                {!editing && (
                                    <button class="edit-icon-btn" onClick={handleEdit} title="Edit page">
                                        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" width="20" height="20"><path d="M22 5.90244L18.0976 2L15.3935 4.70407L19.2959 8.60651L22 5.90244Z"/><path d="M6 18L10.2927 17.6098L17.6797 10.2228L13.7772 6.32032L6.39024 13.7073L6 18Z"/><path fill-rule="evenodd" clip-rule="evenodd" d="M15 22H2V20H15V22Z"/></svg>
                                    </button>
                                )}
                            </div>
                            <div class="page-meta">
                                <span><Highlighted text={current.path} query={searchQuery} /></span>
                                {current.modified_at && <span>{new Date(current.modified_at).toLocaleDateString()}</span>}
                                {current.links && current.links.length > 0 && (
                                    <span>{current.links.length} links</span>
                                )}
                            </div>
                            <div class="page-actions">
                                {editing && (
                                    <>
                                        <button class="btn primary" onClick={handleSave}>save</button>
                                        <button class="btn" onClick={() => setEditing(false)}>cancel</button>
                                    </>
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
                                        dangerouslySetInnerHTML={{ __html: renderedBodyHTML }}
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
                    <div class="empty">
                        <Logo size={96} />
                        <span>select a page</span>
                    </div>
                )}
            </div>
        </div>
    );
}
