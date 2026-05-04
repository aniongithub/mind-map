/** REST API client for mind-map's /api endpoints */

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
        return res.json();
    }

    async searchPages(query: string, limit = 20): Promise<SearchResult[]> {
        const res = await fetch(`/api/search?q=${encodeURIComponent(query)}&limit=${limit}`);
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        return res.json();
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
        return res.json();
    }
}

export const api = new APIClient();
