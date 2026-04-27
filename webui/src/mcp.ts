/** MCP client over SSE — talks to mind-map's /mcp endpoint */

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

type MCPResult = { content: Array<{ type: string; text: string }> };

class MCPClient {
    private endpoint: string;
    private sessionEndpoint: string | null = null;
    private requestId = 0;
    private pending = new Map<number, { resolve: (v: any) => void; reject: (e: any) => void }>();
    private eventSource: EventSource | null = null;
    private ready: Promise<void>;
    private resolveReady!: () => void;

    constructor(endpoint: string) {
        this.endpoint = endpoint;
        this.ready = new Promise(r => { this.resolveReady = r; });
        this.connect();
    }

    private connect() {
        this.eventSource = new EventSource(this.endpoint);

        this.eventSource.addEventListener('endpoint', (e: MessageEvent) => {
            const base = new URL(this.endpoint, window.location.href);
            this.sessionEndpoint = new URL(e.data, base).href;
            this.initialize();
        });

        this.eventSource.addEventListener('message', (e: MessageEvent) => {
            try {
                const msg = JSON.parse(e.data);
                if (msg.id !== undefined && this.pending.has(msg.id)) {
                    const p = this.pending.get(msg.id)!;
                    this.pending.delete(msg.id);
                    if (msg.error) {
                        p.reject(new Error(msg.error.message || 'MCP error'));
                    } else {
                        p.resolve(msg.result);
                    }
                }
            } catch { /* ignore non-JSON */ }
        });

        this.eventSource.onerror = () => {
            console.error('SSE connection lost, reconnecting...');
            setTimeout(() => this.connect(), 2000);
        };
    }

    private async initialize() {
        await this.send('initialize', {
            protocolVersion: '2024-11-05',
            capabilities: {},
            clientInfo: { name: 'mind-map-webui', version: '0.1.0' },
        });
        await this.send('notifications/initialized', undefined, true);
        this.resolveReady();
    }

    private send(method: string, params?: any, isNotification = false): Promise<any> {
        return new Promise((resolve, reject) => {
            if (!this.sessionEndpoint) {
                reject(new Error('Not connected'));
                return;
            }

            const id = isNotification ? undefined : ++this.requestId;
            const msg: any = { jsonrpc: '2.0', method };
            if (id !== undefined) msg.id = id;
            if (params !== undefined) msg.params = params;

            if (id !== undefined) {
                this.pending.set(id, { resolve, reject });
            }

            fetch(this.sessionEndpoint, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(msg),
            }).then(r => {
                if (!r.ok) reject(new Error(`HTTP ${r.status}`));
                if (isNotification) resolve(undefined);
            }).catch(reject);
        });
    }

    async callTool(name: string, args: Record<string, any> = {}): Promise<string> {
        await this.ready;
        const result = await this.send('tools/call', { name, arguments: args }) as MCPResult;
        const text = result.content?.find((c: any) => c.type === 'text');
        return text?.text || '';
    }

    async getWikiContext(): Promise<WikiContext> {
        const text = await this.callTool('get_wiki_context');
        return JSON.parse(text);
    }

    async getPage(path: string): Promise<Page> {
        const text = await this.callTool('get_page', { path });
        return JSON.parse(text);
    }

    async listPages(prefix = ''): Promise<Page[]> {
        const text = await this.callTool('list_pages', { prefix });
        return JSON.parse(text) || [];
    }

    async searchPages(query: string, limit = 20): Promise<SearchResult[]> {
        const text = await this.callTool('search_pages', { query, limit });
        return JSON.parse(text) || [];
    }

    async createPage(path: string, content: string): Promise<void> {
        await this.callTool('create_page', { path, content });
    }

    async updatePage(path: string, content: string): Promise<void> {
        await this.callTool('update_page', { path, content });
    }

    async deletePage(path: string): Promise<void> {
        await this.callTool('delete_page', { path });
    }

    async getBacklinks(path: string): Promise<string[]> {
        const text = await this.callTool('get_backlinks', { path });
        return JSON.parse(text) || [];
    }
}

export const mcp = new MCPClient('/mcp');
