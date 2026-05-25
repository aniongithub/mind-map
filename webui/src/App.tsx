import { useState, useEffect, useRef, useMemo } from 'preact/hooks';
import { api, Page, ReindexStats } from './api';
import { Logo } from './Logo';
import { PageBrowser } from './PageBrowser';
import { GraphView } from './GraphView';
import { searchTokens, searchRegex, Highlighted } from './search';
import { TagInput } from './TagInput';
import { marked } from 'marked';
import mermaid from 'mermaid';

mermaid.initialize({ startOnLoad: false, theme: 'default' });

interface SyncSettings {
    enabled: boolean;
    default: string;
    interval: string;
    mappings?: { prefix: string; remote: string }[];
}

// DigestSettings mirrors internal/config.DigestConfig. All fields are
// optional on the wire — a config file without a digest section
// loads with zero values, which the consumers interpret as "use the
// built-in defaults". The UI surfaces the same contract: empty
// numeric inputs and an empty tag list keep server defaults intact.
interface DigestSettings {
    cloud_size?: number;
    recents_size?: number;
    cloud_refresh?: string;
    stopwords_extra?: string[];
    max_render_bytes?: number;
}

interface Settings {
    sync: SyncSettings;
    digest?: DigestSettings;
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

    // Reindex state (settings panel). reindexResult is null until the
    // first run completes; reindexError holds the most recent failure.
    const [reindexing, setReindexing] = useState(false);
    const [reindexResult, setReindexResult] = useState<ReindexStats | null>(null);
    const [reindexError, setReindexError] = useState<string | null>(null);
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
    // either the filtered list (if a query was saved) or the full page
    // list. The URL takes precedence over localStorage (handled in the
    // hash routing effect below).
    useEffect(() => {
        localStorage.setItem('mm-search-query', searchQuery);
    }, [searchQuery]);

    // Mirror searchQuery into the URL hash so the filter is bookmarkable
    // and reloads restore it. Live typing replaces in place so we don't
    // pollute history with every keystroke — explicit commits (Enter,
    // clicking a gray node) call commitSearch() instead to push a new
    // entry.
    useEffect(() => {
        const current = parseHash();
        if (current.query === searchQuery) return;
        const newHash = buildHash(current.path, searchQuery);
        window.history.replaceState(null, '', '#' + newHash);
    }, [searchQuery]);

    // Push a new history entry, then update the query. Use this for
    // user actions that "commit" a filter so Back undoes the action.
    const commitSearch = (query: string) => {
        const current = parseHash();
        if (current.query !== query) {
            window.history.pushState(null, '', '#' + buildHash(current.path, query));
        }
        setSearchQuery(query);
    };

    useEffect(() => {
        if (searchQuery.trim()) {
            handleSearch();
        } else {
            loadPages();
        }
    }, []);

    // Hash routing: #/path/to/page?q=filter
    // The hash carries two pieces of state: the currently-open page
    // (or null for the graph view) and the active search filter. Both
    // are reflected in the URL so back/forward navigation and reloads
    // restore the same view.
    const parseHash = (): { path: string | null; query: string } => {
        const raw = window.location.hash.replace(/^#/, '');
        const qIdx = raw.indexOf('?');
        const pathPart = (qIdx >= 0 ? raw.slice(0, qIdx) : raw).replace(/^\//, '');
        const queryPart = qIdx >= 0 ? raw.slice(qIdx + 1) : '';
        const params = new URLSearchParams(queryPart);
        return { path: pathPart || null, query: params.get('q') || '' };
    };

    const buildHash = (path: string | null, query: string): string => {
        const p = path ? `/${path}` : '/';
        const q = query.trim();
        return q ? `${p}?q=${encodeURIComponent(q)}` : p;
    };

    useEffect(() => {
        const onHash = () => {
            const { path, query } = parseHash();
            if (path) openPage(path);
            else setCurrent(null);
            // setState is a no-op when the value matches, so this won't
            // ping-pong with the searchQuery→URL writer effect below.
            setSearchQuery(query);
        };
        window.addEventListener('hashchange', onHash);

        // Load initial state. URL wins over localStorage: a query in
        // the hash means the user is sharing/reloading a filtered view
        // and we should honor it exactly.
        const initial = parseHash();
        if (initial.path) openPage(initial.path);
        if (initial.query) setSearchQuery(initial.query);

        return () => window.removeEventListener('hashchange', onHash);
    }, []);

    const navigate = (path: string | null) => {
        // Preserve the active search filter across navigations so that
        // clicking a result in a filtered sidebar opens the page with
        // the same filter still applied.
        window.location.hash = buildHash(path, searchQuery);
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
        // Pressing Enter is an explicit commit — push a history entry
        // for the current query so Back undoes the filter session.
        const current = parseHash();
        if (current.query !== searchQuery) {
            window.history.pushState(null, '', '#' + buildHash(current.path, searchQuery));
        }
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

    const handleReindex = async () => {
        setReindexing(true);
        setReindexError(null);
        try {
            const stats = await api.reindex();
            setReindexResult(stats);
            // The list of pages may have changed (files written
            // outside the API are now indexed); refresh so the sidebar
            // reflects the new state.
            await loadPages();
        } catch (e) {
            console.error('Reindex failed:', e);
            setReindexError(e instanceof Error ? e.message : String(e));
        } finally {
            setReindexing(false);
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

    // updateDigest is the per-field mutator for the Digest section.
    // It accepts the field's actual value type (number for numeric
    // knobs, string for cloud_refresh, string[] for stopwords). The
    // server omits the digest section entirely when it's never been
    // set, so we lazily materialize an empty object on first touch.
    const updateDigest = <K extends keyof DigestSettings>(field: K, value: DigestSettings[K]) => {
        if (!settings) return;
        setSettings({
            ...settings,
            digest: { ...(settings.digest ?? {}), [field]: value },
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

        // Rewrite wiki-local image references to point at the asset
        // handler. Goldmark image syntax `![alt](path)` lands here
        // pre-render so marked still parses normally and gets a
        // well-formed `<img>` tag with the corrected `src`. We touch
        // only paths the asset handler can serve: external URLs
        // (http/https/data/mailto/…), in-page anchors (#foo) and
        // empty destinations are left alone.
        const withAssets = withLinks.replace(
            /!\[([^\]]*)\]\(([^)\s]+)(\s+"[^"]*")?\)/g,
            (match, alt: string, dest: string, title: string | undefined) => {
                if (!isWikiLocalImageRef(dest)) return match;
                // Strip any leading "./" so we don't end up with
                // "/assets/./..." which most servers normalize but
                // looks ugly in DOM inspections.
                const cleaned = dest.replace(/^\.\//, '');
                const url = '/assets/' + cleaned.replace(/^\/+/, '');
                return `![${alt}](${url}${title ?? ''})`;
            }
        );

        // Extract mermaid blocks before marked processing to prevent HTML escaping
        const mermaidBlocks: Record<string, string> = {};
        let localCounter = 0;
        const withPlaceholders = withAssets.replace(/```mermaid\s*\n([\s\S]*?)```/g, (_, code) => {
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

    // isWikiLocalImageRef mirrors the Go-side isWikiLocalRef used by
    // the indexer (internal/wiki/parse.go): rejects external URLs,
    // anchor-only refs, and empty destinations. Anything else is a
    // path the static asset handler can serve.
    function isWikiLocalImageRef(dest: string): boolean {
        if (!dest) return false;
        if (dest.startsWith('#')) return false;
        // RFC 3986 scheme detection: a ':' before any '/', '?', or
        // '#' means an absolute URL.
        for (let i = 0; i < dest.length; i++) {
            const c = dest[i];
            if (c === ':') return false;
            if (c === '/' || c === '?' || c === '#') break;
        }
        return true;
    }

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
                    <a
                        href="#/"
                        class="sidebar-header-text"
                        onClick={(e) => { e.preventDefault(); navigate(null); }}
                        title="Show graph view"
                    >mind-map</a>
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

                            <div class="settings-section">
                                <div class="settings-section-title">Digest</div>
                                <div class="settings-field-help">
                                    The per-conversation orientation digest summarizes what this wiki is about. A background job rebuilds the word/phrase cloud on a schedule; the recents LRU updates on every page op. All fields are optional &mdash; leave blank to use defaults.
                                </div>

                                <div class="settings-field">
                                    <label>Extra Stopwords</label>
                                    <div class="hint">
                                        Domain-specific noise to exclude from the cloud (e.g. <code>TODO</code>, <code>FIXME</code>, <code>see</code>, <code>also</code>). Comma, space, or Enter to add a tag; Backspace on empty input removes the last one.
                                    </div>
                                    <TagInput
                                        value={settings.digest?.stopwords_extra ?? []}
                                        onChange={(next) => updateDigest('stopwords_extra', next)}
                                        placeholder="Type a word and press space, comma, or Enter"
                                    />
                                </div>

                                <div class="settings-field">
                                    <label>Cloud Size</label>
                                    <div class="hint">Top-K terms in the word/phrase cloud. Default 50.</div>
                                    <input
                                        type="number"
                                        min="1"
                                        value={settings.digest?.cloud_size ?? ''}
                                        onInput={(e) => {
                                            const v = (e.target as HTMLInputElement).value;
                                            updateDigest('cloud_size', v === '' ? undefined : parseInt(v, 10));
                                        }}
                                        placeholder="50"
                                    />
                                </div>

                                <div class="settings-field">
                                    <label>Recents Size</label>
                                    <div class="hint">Active-use LRU capacity. Default 20. Applied on next restart.</div>
                                    <input
                                        type="number"
                                        min="1"
                                        value={settings.digest?.recents_size ?? ''}
                                        onInput={(e) => {
                                            const v = (e.target as HTMLInputElement).value;
                                            updateDigest('recents_size', v === '' ? undefined : parseInt(v, 10));
                                        }}
                                        placeholder="20"
                                    />
                                </div>

                                <div class="settings-field">
                                    <label>Cloud Refresh Interval</label>
                                    <div class="hint">How often the cloud rebuilds (e.g. 5m, 10m). Floor: 30s. Default 5m.</div>
                                    <input
                                        type="text"
                                        value={settings.digest?.cloud_refresh ?? ''}
                                        onInput={(e) => updateDigest('cloud_refresh', (e.target as HTMLInputElement).value || undefined)}
                                        placeholder="5m"
                                    />
                                </div>

                                <div class="settings-field">
                                    <label>Max Render Bytes</label>
                                    <div class="hint">Soft cap on the rendered markdown blob. Default 4096 (~1K tokens). Set to 0 to disable trimming.</div>
                                    <input
                                        type="number"
                                        min="0"
                                        value={settings.digest?.max_render_bytes ?? ''}
                                        onInput={(e) => {
                                            const v = (e.target as HTMLInputElement).value;
                                            updateDigest('max_render_bytes', v === '' ? undefined : parseInt(v, 10));
                                        }}
                                        placeholder="4096"
                                    />
                                </div>
                            </div>

                            <div class="settings-section">
                                <div class="settings-section-title">Index</div>
                                <div class="settings-field-help">
                                    The wiki keeps a search index over the on-disk markdown files. It updates automatically
                                    on writes and on every sync pull. Use this if you've edited files outside the wiki
                                    (e.g. directly on disk) and want the index to catch up without restarting.
                                </div>
                                <div class="settings-reindex-row">
                                    <button
                                        class="btn"
                                        type="button"
                                        onClick={handleReindex}
                                        disabled={reindexing}
                                    >
                                        {reindexing ? 'Reindexing…' : 'Reindex now'}
                                    </button>
                                    {reindexResult && !reindexing && !reindexError && (
                                        <div class="settings-reindex-status">
                                            {reindexResult.total} pages
                                            {' · '}
                                            +{reindexResult.added} / ~{reindexResult.updated} / −{reindexResult.removed}
                                            {' · '}
                                            {reindexResult.elapsed_ms} ms
                                        </div>
                                    )}
                                    {reindexError && (
                                        <div class="settings-reindex-error">{reindexError}</div>
                                    )}
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
                    <GraphView
                        pages={rawPages}
                        searchQuery={searchQuery}
                        onNavigate={navigate}
                        onSearch={commitSearch}
                    />
                )}
            </div>
        </div>
    );
}
