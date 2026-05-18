import { useEffect, useRef, useState, useMemo } from 'preact/hooks';
import ForceGraph from 'force-graph';
import { Page, Link, api } from './mcp';
import { searchTokens, searchRegex } from './search';

type EdgeKind = 'path' | 'reference';

interface GraphNode {
    id: string;
    label: string;
    isPage: boolean;
    page?: Page;
    val?: number;
    // Mutated by force-graph at runtime:
    x?: number;
    y?: number;
}

interface GraphLink {
    source: string;
    target: string;
    kind: EdgeKind;
}

// Build nodes (one per unique path prefix) and edges (path hierarchy +
// page-to-page references) from the page list and the all-links table.
function buildGraph(pages: Page[], refs: Link[]): { nodes: GraphNode[]; links: GraphLink[] } {
    const nodeMap = new Map<string, GraphNode>();
    const ensureNode = (id: string, page?: Page) => {
        let n = nodeMap.get(id);
        if (!n) {
            const segs = id.split('/');
            n = { id, label: segs[segs.length - 1], isPage: false };
            nodeMap.set(id, n);
        }
        if (page) {
            n.isPage = true;
            n.page = page;
            n.label = page.title || n.label;
        }
        return n;
    };

    const pathLinks: GraphLink[] = [];
    for (const p of pages) {
        const parts = p.path.split('/');
        let accum = '';
        let prev = '';
        for (let i = 0; i < parts.length; i++) {
            accum = accum ? accum + '/' + parts[i] : parts[i];
            ensureNode(accum, i === parts.length - 1 ? p : undefined);
            if (prev) pathLinks.push({ source: prev, target: accum, kind: 'path' });
            prev = accum;
        }
    }

    const refLinks: GraphLink[] = [];
    for (const r of refs) {
        // Both endpoints must already be reachable as nodes. If a
        // wikilink targets a non-existent page we still want to show
        // the broken edge — create the node as a folder placeholder.
        ensureNode(r.source);
        ensureNode(r.target);
        if (r.source !== r.target) {
            refLinks.push({ source: r.source, target: r.target, kind: 'reference' });
        }
    }

    return { nodes: [...nodeMap.values()], links: [...pathLinks, ...refLinks] };
}

function readCssVar(name: string): string {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || '#888';
}

// Greedy word-wrap a label to a maximum width (in canvas units).
// Words that on their own exceed maxWidth pass through unbroken; we
// don't try to hyphenate, the goal is readability not perfection.
function wrapLabel(ctx: CanvasRenderingContext2D, text: string, maxWidth: number): string[] {
    if (!text) return [''];
    if (ctx.measureText(text).width <= maxWidth) return [text];

    const words = text.split(/\s+/);
    const lines: string[] = [];
    let current = '';
    for (const w of words) {
        const candidate = current ? current + ' ' + w : w;
        if (ctx.measureText(candidate).width <= maxWidth) {
            current = candidate;
        } else {
            if (current) lines.push(current);
            current = w;
        }
    }
    if (current) lines.push(current);
    return lines;
}

interface GraphViewProps {
    pages: Page[];
    searchQuery: string;
    onNavigate: (path: string) => void;
    onSearch: (query: string) => void;
}

export function GraphView({ pages, searchQuery, onNavigate, onSearch }: GraphViewProps) {
    const containerRef = useRef<HTMLDivElement>(null);
    const graphRef = useRef<any>(null);
    const [refs, setRefs] = useState<Link[]>([]);
    const [showPaths, setShowPaths] = useState(() => localStorage.getItem('mm-graph-show-paths') !== 'false');
    const [showRefs, setShowRefs] = useState(() => localStorage.getItem('mm-graph-show-refs') !== 'false');

    useEffect(() => {
        localStorage.setItem('mm-graph-show-paths', String(showPaths));
    }, [showPaths]);
    useEffect(() => {
        localStorage.setItem('mm-graph-show-refs', String(showRefs));
    }, [showRefs]);

    // Reference edges still come from the full /api/links table — they
    // describe wikilink relationships, not search-filtered subsets.
    useEffect(() => {
        let cancelled = false;
        api.allLinks().then(l => {
            if (cancelled) return;
            setRefs(l);
        }).catch(e => console.error('graph links load failed:', e));
        return () => { cancelled = true; };
    }, []);

    const fullGraph = useMemo(() => buildGraph(pages, refs), [pages, refs]);

    // Filter by search query: a node passes if its label, page title,
    // or any path segment matches a search token. Folder nodes that
    // contain a matching descendant also pass so the path stays
    // visible. Edges are kept only when both endpoints pass.
    const visibleGraph = useMemo(() => {
        const re = searchRegex(searchTokens(searchQuery));
        let allowedIds: Set<string> | null = null;
        if (re) {
            const matches = new Set<string>();
            for (const n of fullGraph.nodes) {
                const haystack = `${n.id} ${n.label} ${n.page?.title || ''}`;
                re.lastIndex = 0;
                if (re.test(haystack)) {
                    // Add the node and every ancestor path-prefix so the
                    // chain back to root remains drawable.
                    const parts = n.id.split('/');
                    let accum = '';
                    for (const p of parts) {
                        accum = accum ? accum + '/' + p : p;
                        matches.add(accum);
                    }
                }
            }
            allowedIds = matches;
        }

        const nodes = allowedIds
            ? fullGraph.nodes.filter(n => allowedIds!.has(n.id))
            : fullGraph.nodes;

        const links = fullGraph.links.filter(l => {
            if (l.kind === 'path' && !showPaths) return false;
            if (l.kind === 'reference' && !showRefs) return false;
            if (allowedIds) {
                const src = typeof l.source === 'string' ? l.source : (l.source as any).id;
                const tgt = typeof l.target === 'string' ? l.target : (l.target as any).id;
                if (!allowedIds.has(src) || !allowedIds.has(tgt)) return false;
            }
            return true;
        });

        return { nodes, links };
    }, [fullGraph, showPaths, showRefs, searchQuery]);

    // Mount the force-graph instance once; feed it new data on changes.
    useEffect(() => {
        if (!containerRef.current) return;

        // Hold theme colors in a ref so per-frame callbacks always read
        // the latest values when the user toggles light/dark.
        const colors = {
            accent: readCssVar('--accent'),
            fg: readCssVar('--fg'),
            fgDim: readCssVar('--fg-dim'),
            edgePath: readCssVar('--graph-edge-path'),
            edgeRef: readCssVar('--graph-edge-ref'),
            font: readCssVar('--font') || 'Inter, sans-serif',
        };
        const refreshColors = () => {
            colors.accent = readCssVar('--accent');
            colors.fg = readCssVar('--fg');
            colors.fgDim = readCssVar('--fg-dim');
            colors.edgePath = readCssVar('--graph-edge-path');
            colors.edgeRef = readCssVar('--graph-edge-ref');
            colors.font = readCssVar('--font') || 'Inter, sans-serif';
        };

        const el = containerRef.current;
        const g: any = new (ForceGraph as any)(el);
        graphRef.current = g;

        g
            .width(el.clientWidth || 800)
            .height(el.clientHeight || 600)
            .backgroundColor('rgba(0,0,0,0)')
            .nodeRelSize(2.5)
            .nodeVal((n: GraphNode) => (n.isPage ? 1.5 : 0.6))
            .nodeColor((n: GraphNode) => (n.isPage ? colors.accent : colors.fgDim))
            .nodeLabel((n: GraphNode) => n.page?.title || n.label || n.id)
            .linkColor((l: GraphLink) => (l.kind === 'reference' ? colors.edgeRef : colors.edgePath))
            .linkWidth((l: GraphLink) => (l.kind === 'reference' ? 2 : 1))
            .linkDirectionalArrowLength((l: GraphLink) => (l.kind === 'reference' ? 4 : 0))
            .linkDirectionalArrowRelPos(0.9)
            .nodeCanvasObjectMode(() => 'after')
            .nodeCanvasObject((node: GraphNode, ctx: CanvasRenderingContext2D, globalScale: number) => {
                if (globalScale < 1.4) return;
                const label = node.page?.title || node.label || node.id;
                const fontSize = 11 / globalScale;
                // Metro-style: light weight, clean sans-serif.
                ctx.font = `300 ${fontSize}px ${colors.font}`;
                const radius = Math.sqrt(node.isPage ? 1.5 : 0.6) * 2.5;
                const gap = 2 / globalScale;
                const maxWidth = 80 / globalScale;
                const lineHeight = fontSize * 1.15;
                const lines = wrapLabel(ctx, label, maxWidth);

                ctx.textAlign = 'center';
                ctx.textBaseline = 'top';
                ctx.fillStyle = colors.fg;
                const startY = (node.y || 0) + radius + gap;
                for (let i = 0; i < lines.length; i++) {
                    ctx.fillText(lines[i], node.x || 0, startY + i * lineHeight);
                }
            })
            .onNodeClick((n: GraphNode) => {
                if (n.page) onNavigate(n.id);
                else onSearch(n.id);
            });

        // Watch <html> for theme class changes; refresh cached colors
        // and nudge the simulation so the canvas repaints with the new
        // palette even if the layout has already cooled down.
        const themeObserver = new MutationObserver(() => {
            refreshColors();
            if (graphRef.current) {
                // Re-feed the current data to force-graph to trigger a
                // repaint with the new colors.
                graphRef.current.graphData(graphRef.current.graphData());
            }
        });
        themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] });

        // Track container size for ResizeObserver-driven re-fit.
        const ro = new ResizeObserver(() => {
            if (!containerRef.current) return;
            g.width(containerRef.current.clientWidth);
            g.height(containerRef.current.clientHeight);
        });
        ro.observe(el);

        return () => {
            ro.disconnect();
            themeObserver.disconnect();
            if (typeof g._destructor === 'function') g._destructor();
            el.innerHTML = '';
            graphRef.current = null;
        };
    }, []);

    // Push data into the graph whenever visible-graph changes.
    useEffect(() => {
        if (!graphRef.current) return;
        graphRef.current.graphData(visibleGraph);
    }, [visibleGraph]);

    return (
        <div class="graph-view">
            <div class="graph-toolbar">
                <label class="graph-toggle">
                    <input
                        type="checkbox"
                        checked={showPaths}
                        onChange={(e) => setShowPaths((e.target as HTMLInputElement).checked)}
                    />
                    <span class="graph-swatch graph-swatch-path" aria-hidden="true"></span>
                    paths
                </label>
                <label class="graph-toggle">
                    <input
                        type="checkbox"
                        checked={showRefs}
                        onChange={(e) => setShowRefs((e.target as HTMLInputElement).checked)}
                    />
                    <span class="graph-swatch graph-swatch-ref" aria-hidden="true"></span>
                    references
                </label>
            </div>
            <div class="graph-canvas" ref={containerRef} />
        </div>
    );
}
