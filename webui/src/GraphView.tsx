import { useEffect, useRef, useState, useMemo } from 'preact/hooks';
import ForceGraph from 'force-graph';
import { Page, Link, api } from './mcp';

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

interface GraphViewProps {
    onNavigate: (path: string) => void;
}

export function GraphView({ onNavigate }: GraphViewProps) {
    const containerRef = useRef<HTMLDivElement>(null);
    const graphRef = useRef<any>(null);
    const [pages, setPages] = useState<Page[]>([]);
    const [refs, setRefs] = useState<Link[]>([]);
    const [showPaths, setShowPaths] = useState(() => localStorage.getItem('mm-graph-show-paths') !== 'false');
    const [showRefs, setShowRefs] = useState(() => localStorage.getItem('mm-graph-show-refs') !== 'false');

    useEffect(() => {
        localStorage.setItem('mm-graph-show-paths', String(showPaths));
    }, [showPaths]);
    useEffect(() => {
        localStorage.setItem('mm-graph-show-refs', String(showRefs));
    }, [showRefs]);

    // Fetch pages + links once on mount.
    useEffect(() => {
        let cancelled = false;
        Promise.all([api.listPages(), api.allLinks()]).then(([p, l]) => {
            if (cancelled) return;
            setPages(p);
            setRefs(l);
        }).catch(e => console.error('graph load failed:', e));
        return () => { cancelled = true; };
    }, []);

    const fullGraph = useMemo(() => buildGraph(pages, refs), [pages, refs]);

    const visibleGraph = useMemo(() => {
        const links = fullGraph.links.filter(l =>
            (l.kind === 'path' && showPaths) || (l.kind === 'reference' && showRefs)
        );
        return { nodes: fullGraph.nodes, links };
    }, [fullGraph, showPaths, showRefs]);

    // Mount the force-graph instance once; feed it new data on changes.
    useEffect(() => {
        if (!containerRef.current) return;
        const accent = readCssVar('--accent');
        const fg = readCssVar('--fg');
        const fgDim = readCssVar('--fg-dim');
        const border = readCssVar('--border');

        const el = containerRef.current;
        const g: any = new (ForceGraph as any)(el);
        graphRef.current = g;

        g
            .width(el.clientWidth || 800)
            .height(el.clientHeight || 600)
            .backgroundColor('rgba(0,0,0,0)')
            .nodeRelSize(4)
            .nodeVal((n: GraphNode) => (n.isPage ? 4 : 2))
            .nodeColor((n: GraphNode) => (n.isPage ? accent : fgDim))
            .nodeLabel((n: GraphNode) => n.page?.title || n.label || n.id)
            .linkColor((l: GraphLink) => (l.kind === 'reference' ? accent : border))
            .linkWidth((l: GraphLink) => (l.kind === 'reference' ? 1.5 : 0.5))
            .linkDirectionalArrowLength((l: GraphLink) => (l.kind === 'reference' ? 4 : 0))
            .linkDirectionalArrowRelPos(0.9)
            .nodeCanvasObjectMode(() => 'after')
            .nodeCanvasObject((node: GraphNode, ctx: CanvasRenderingContext2D, globalScale: number) => {
                // Only draw labels above a zoom threshold so the graph
                // stays readable when fully zoomed out.
                if (globalScale < 1.4) return;
                const label = node.page?.title || node.label || node.id;
                const fontSize = 11 / globalScale;
                ctx.font = `${fontSize}px Inter, sans-serif`;
                // Always use --fg for label text. Folder nodes are filled
                // with --fg-dim, so labeling them in --fg-dim made them
                // invisible against their own background.
                ctx.fillStyle = fg;
                ctx.textAlign = 'center';
                ctx.textBaseline = 'middle';
                ctx.fillText(label, node.x || 0, node.y || 0);
            })
            .onNodeClick((n: GraphNode) => {
                if (n.page) onNavigate(n.id);
            });

        // Track container size for ResizeObserver-driven re-fit.
        const ro = new ResizeObserver(() => {
            if (!containerRef.current) return;
            g.width(containerRef.current.clientWidth);
            g.height(containerRef.current.clientHeight);
        });
        ro.observe(el);

        return () => {
            ro.disconnect();
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
