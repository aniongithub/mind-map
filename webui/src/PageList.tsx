import { Page } from './api';
import { Highlighted } from './search';

interface PageListProps {
    pages: Page[];
    searchQuery: string;
    currentPath?: string;
    onNavigate: (path: string) => void;
}

// Flat list view. Used by the "Recent" and "A→Z title" sort modes.
export function PageList({ pages, searchQuery, currentPath, onNavigate }: PageListProps) {
    return (
        <ul class="page-list">
            {pages.map(p => (
                <li
                    key={p.path}
                    class={`page-item ${currentPath === p.path ? 'active' : ''}`}
                    onClick={() => onNavigate(p.path)}
                >
                    <div class="page-item-title">
                        <Highlighted text={p.title || p.path} query={searchQuery} />
                    </div>
                    <div class="page-item-path">
                        <Highlighted text={p.path} query={searchQuery} />
                    </div>
                </li>
            ))}
        </ul>
    );
}
