/**
 * REST API client for mind-map's /api/* endpoints.
 *
 * Note: this is NOT an MCP client. The web UI talks to the Go HTTP server
 * over plain JSON REST. MCP is only used by AI agents over stdio.
 */

export interface Page {
    path: string;
    title: string;
    body: string;
    frontmatter?: Record<string, any>;
    links?: string[];
    backlinks?: string[];
    modified_at?: string;
}

export interface SearchResult {
    path: string;
    title: string;
    snippet: string;
}

export interface WikiContext {
    page_count: number;
    recent_pages: Page[];
    top_level_dirs: string[];
}

/**
 * Stats returned by a reindex pass. Mirrors wiki.ReindexStats on the
 * server. `total` is the count of markdown files found on disk; the
 * change counts (added/updated/removed) sum with `unchanged` to `total`.
 */
export interface ReindexStats {
    total: number;
    added: number;
    updated: number;
    removed: number;
    unchanged: number;
    elapsed_ms: number;
}

class APIClient {
    async getWikiContext(): Promise<WikiContext> {
        const res = await fetch('/api/context');
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        return res.json();
    }

    async getPage(path: string): Promise<Page> {
        const res = await fetch(`/api/pages/${path}`);
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        return res.json();
    }

    async listPages(prefix = ''): Promise<Page[]> {
        const url = prefix ? `/api/pages?prefix=${encodeURIComponent(prefix)}` : '/api/pages';
        const res = await fetch(url);
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        return (await res.json()) || [];
    }

    async searchPages(query: string, limit = 20): Promise<SearchResult[]> {
        const res = await fetch(`/api/search?q=${encodeURIComponent(query)}&limit=${limit}`);
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        return (await res.json()) || [];
    }

    async createPage(path: string, content: string): Promise<void> {
        const res = await fetch('/api/pages', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ path, content }),
        });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
    }

    async updatePage(path: string, content: string): Promise<void> {
        const res = await fetch(`/api/pages/${path}`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ content }),
        });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
    }

    async deletePage(path: string): Promise<void> {
        const res = await fetch(`/api/pages/${path}`, { method: 'DELETE' });
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
    }

    async getBacklinks(path: string): Promise<string[]> {
        const res = await fetch(`/api/backlinks/${path}`);
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        return (await res.json()) || [];
    }

    async allLinks(): Promise<Link[]> {
        const res = await fetch('/api/links');
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        return (await res.json()) || [];
    }

    /**
     * Force a full reindex pass over the on-disk wiki. Use when files
     * have been edited outside the wiki API (e.g. directly on disk via
     * an editor or a sync that produced files in unusual ways) and the
     * server's index needs to catch up.
     *
     * Returns stats: how many pages were added, updated, removed,
     * left unchanged, and how long the pass took.
     */
    async reindex(): Promise<ReindexStats> {
        const res = await fetch('/api/reindex', { method: 'POST' });
        if (!res.ok) {
            const text = await res.text().catch(() => '');
            throw new Error(`HTTP ${res.status}${text ? `: ${text.trim()}` : ''}`);
        }
        return res.json();
    }

    /**
     * Fetch available export formats and their settings schemas.
     */
    async exportFormats(): Promise<ExportFormat[]> {
        const res = await fetch('/api/export/formats');
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        return (await res.json()) || [];
    }

    /**
     * Trigger a file download for the given export configuration.
     * Opens the export URL in a new tab/download.
     */
    exportUrl(format: string, page: string, depth?: number, settings?: Record<string, any>): string {
        const params = new URLSearchParams();
        params.set('format', format);
        params.set('page', page);
        if (depth !== undefined) params.set('depth', String(depth));
        if (settings) {
            for (const [key, value] of Object.entries(settings)) {
                params.set(key, String(value));
            }
        }
        return `/api/export?${params.toString()}`;
    }
}

export interface ExportFormat {
    name: string;
    description: string;
    content_type: string;
    extension: string;
    settings: {
        fields: ExportSettingsField[];
    };
}

export interface ExportSettingsField {
    key: string;
    label: string;
    description?: string;
    type: 'bool' | 'int' | 'string' | 'enum';
    default: any;
    enum?: string[];
}

export interface Link {
    source: string;
    target: string;
}

export const api = new APIClient();
