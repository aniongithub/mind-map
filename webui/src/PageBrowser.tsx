import { useState, useEffect, useMemo } from 'preact/hooks';
import { Page } from './mcp';
import { PageList } from './PageList';
import { PageTree } from './PageTree';
import { SortToggle, SortMode, sortModes } from './SortToggle';

interface PageBrowserProps {
    pages: Page[];
    searchQuery: string;
    onSearchChange: (q: string) => void;
    onSearchSubmit: () => void;
    onSearchClear: () => void;
    currentPath?: string;
    onNavigate: (path: string) => void;
}

// Search input + sort cycle + page-list-or-tree. Owns sortMode and
// the rendered-list-vs-tree decision so the rest of the app doesn't
// have to thread that state around.
export function PageBrowser({
    pages,
    searchQuery,
    onSearchChange,
    onSearchSubmit,
    onSearchClear,
    currentPath,
    onNavigate,
}: PageBrowserProps) {
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

    const sortedPages = useMemo(() => {
        const sorted = [...pages];
        switch (sortMode) {
            case 'path':
                sorted.sort((a, b) => a.path.localeCompare(b.path));
                break;
            case 'title':
                sorted.sort((a, b) => (a.title || a.path).localeCompare(b.title || b.path));
                break;
            case 'recent':
            default:
                // API already returns modified DESC; preserve that order.
                break;
        }
        return sorted;
    }, [pages, sortMode]);

    return (
        <>
            <div class="sidebar-search">
                <div class="search-wrapper">
                    <div class="search-input-wrap">
                        <input
                            type="text"
                            placeholder="search..."
                            value={searchQuery}
                            onInput={(e) => onSearchChange((e.target as HTMLInputElement).value)}
                            onKeyDown={(e) => { if (e.key === 'Enter') onSearchSubmit(); }}
                        />
                        {searchQuery && (
                            <button
                                class="search-clear"
                                onClick={onSearchClear}
                                title="Clear search"
                                aria-label="Clear search"
                            >
                                &times;
                            </button>
                        )}
                    </div>
                    <SortToggle mode={sortMode} onCycle={cycleSortMode} />
                </div>
            </div>
            {sortMode === 'path' ? (
                <PageTree
                    pages={sortedPages}
                    searchQuery={searchQuery}
                    currentPath={currentPath}
                    onNavigate={onNavigate}
                />
            ) : (
                <PageList
                    pages={sortedPages}
                    searchQuery={searchQuery}
                    currentPath={currentPath}
                    onNavigate={onNavigate}
                />
            )}
        </>
    );
}
