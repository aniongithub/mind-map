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
 * Thrown by `api.movePage` when the destination already exists and
 * `overwrite` was false. Catch this specifically (vs Error) to prompt
 * the user for confirmation and retry with overwrite=true.
 */
export class DestinationExistsError extends Error {
    constructor(public readonly to: string) {
        super(`destination already exists: ${to}`);
        this.name = 'DestinationExistsError';
    }
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

    /**
     * Move (or rename) a page.
     *
     * On HTTP 409 the server signals that the destination already exists.
     * That case is *recoverable*: the caller should prompt the user for
     * confirmation and retry with `overwrite=true`. We surface it as a
     * typed `DestinationExistsError` rather than a generic Error so call
     * sites can branch on it without parsing strings.
     *
     * Any other non-2xx becomes a plain Error with the response text, to
     * match the rest of this client.
     */
    async movePage(from: string, to: string, overwrite = false): Promise<void> {
        const res = await fetch('/api/pages/move', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ from, to, overwrite }),
        });
        if (res.ok) return;

        if (res.status === 409) {
            // Try to parse the structured body; fall back to a generic
            // DestinationExistsError if the body isn't what we expect.
            let body: any = null;
            try { body = await res.json(); } catch { /* ignore */ }
            if (body && body.error === 'destination_exists') {
                throw new DestinationExistsError(to);
            }
        }

        const text = await res.text().catch(() => '');
        throw new Error(`HTTP ${res.status}${text ? `: ${text.trim()}` : ''}`);
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
}

export interface Link {
    source: string;
    target: string;
}

export const api = new APIClient();
