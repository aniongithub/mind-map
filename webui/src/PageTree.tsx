import { useState, useEffect, useMemo } from 'preact/hooks';
import { Page } from './api';
import { Highlighted } from './search';

// Tree node used by the "A→Z path" sort mode. Folders are derived from
// path segments; a node carries a real Page when there's an actual page
// at that path (otherwise it's a folder-only intermediate).
interface TreeNode {
    path: string;
    segment: string;
    page?: Page;
    children: TreeNode[];
}

function buildPageTree(pages: Page[]): TreeNode {
    const root: TreeNode = { path: '', segment: '', children: [] };
    const map = new Map<string, TreeNode>();
    map.set('', root);

    for (const p of pages) {
        const parts = p.path.split('/');
        let accum = '';
        let parent = root;
        for (let i = 0; i < parts.length; i++) {
            accum = accum ? accum + '/' + parts[i] : parts[i];
            let node = map.get(accum);
            if (!node) {
                node = { path: accum, segment: parts[i], children: [] };
                map.set(accum, node);
                parent.children.push(node);
            }
            if (i === parts.length - 1) node.page = p;
            parent = node;
        }
    }

    // Folders first within a level, then alphabetical by segment.
    const sortRec = (n: TreeNode) => {
        n.children.sort((a, b) => {
            const af = a.children.length > 0 ? 0 : 1;
            const bf = b.children.length > 0 ? 0 : 1;
            if (af !== bf) return af - bf;
            return a.segment.localeCompare(b.segment);
        });
        n.children.forEach(sortRec);
    };
    sortRec(root);
    return root;
}

interface PageTreeProps {
    pages: Page[];
    searchQuery: string;
    currentPath?: string;
    onNavigate: (path: string) => void;
}

// Outlook-style tree view. Used by the "A→Z path" sort mode.
//   - One row per path segment; indent reflects depth.
//   - Folders (nodes with children) show a chevron that rotates on toggle.
//   - Clicking a folder name that also has a real page navigates to it;
//     clicking a folder name without a page just toggles expansion.
//   - The chevron always toggles, never navigates.
//   - Collapsed state is persisted in localStorage so reloads keep the
//     user's expansion preferences. Default: everything expanded.
export function PageTree({ pages, searchQuery, currentPath, onNavigate }: PageTreeProps) {
    const tree = useMemo(() => buildPageTree(pages), [pages]);

    const [collapsed, setCollapsed] = useState<Set<string>>(() => {
        try {
            const raw = localStorage.getItem('mm-tree-collapsed');
            if (raw) return new Set(JSON.parse(raw));
        } catch { /* ignore corrupt storage */ }
        return new Set();
    });

    useEffect(() => {
        localStorage.setItem('mm-tree-collapsed', JSON.stringify([...collapsed]));
    }, [collapsed]);

    const toggle = (path: string) => {
        setCollapsed(prev => {
            const next = new Set(prev);
            if (next.has(path)) next.delete(path);
            else next.add(path);
            return next;
        });
    };

    return (
        <ul class="page-list page-tree">
            {tree.children.map(child => (
                <TreeRow
                    key={child.path}
                    node={child}
                    depth={0}
                    collapsed={collapsed}
                    onToggle={toggle}
                    currentPath={currentPath}
                    onNavigate={onNavigate}
                    searchQuery={searchQuery}
                />
            ))}
        </ul>
    );
}

interface TreeRowProps {
    node: TreeNode;
    depth: number;
    collapsed: Set<string>;
    onToggle: (path: string) => void;
    currentPath?: string;
    onNavigate: (path: string) => void;
    searchQuery: string;
}

function TreeRow({ node, depth, collapsed, onToggle, currentPath, onNavigate, searchQuery }: TreeRowProps) {
    const hasChildren = node.children.length > 0;
    const isCollapsed = collapsed.has(node.path);
    const isActive = currentPath === node.path && node.page !== undefined;

    const handleRowClick = () => {
        if (node.page) onNavigate(node.path);
        else if (hasChildren) onToggle(node.path);
    };

    const handleChevronClick = (e: MouseEvent) => {
        e.stopPropagation();
        onToggle(node.path);
    };

    const label = node.page?.title || node.segment;

    return (
        <>
            <li
                class={`tree-row ${isActive ? 'active' : ''} ${node.page ? 'has-page' : 'folder-only'} ${hasChildren ? 'has-children' : 'leaf'}`}
                style={{ paddingLeft: `${8 + depth * 12}px` }}
                onClick={handleRowClick}
            >
                <span
                    class={`tree-chevron ${hasChildren && !isCollapsed ? 'open' : ''}`}
                    onClick={hasChildren ? handleChevronClick : undefined}
                    aria-hidden="true"
                >&#9656;</span>
                <span class="tree-label">
                    <Highlighted text={label} query={searchQuery} />
                </span>
            </li>
            {hasChildren && !isCollapsed && node.children.map(child => (
                <TreeRow
                    key={child.path}
                    node={child}
                    depth={depth + 1}
                    collapsed={collapsed}
                    onToggle={onToggle}
                    currentPath={currentPath}
                    onNavigate={onNavigate}
                    searchQuery={searchQuery}
                />
            ))}
        </>
    );
}
