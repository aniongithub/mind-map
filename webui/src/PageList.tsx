import { Page } from './api';
import { Highlighted } from './search';
import { RowAction, RowActionsButton } from './RowActions';

interface PageListProps {
    pages: Page[];
    searchQuery: string;
    currentPath?: string;
    onNavigate: (path: string) => void;
    /**
     * Called when the user picks an action from a row's ⋯ menu. Optional
     * because some embeddings of PageList (e.g. recent-pages widgets)
     * don't need overflow actions.
     */
    onRowAction?: (action: RowAction, page: Page) => void;
}

// Flat list view. Used by the "Recent" and "A→Z title" sort modes.
export function PageList({ pages, searchQuery, currentPath, onNavigate, onRowAction }: PageListProps) {
    return (
        <ul class="page-list">
            {pages.map(p => (
                <li
                    key={p.path}
                    class={`page-item ${currentPath === p.path ? 'active' : ''}`}
                    onClick={() => onNavigate(p.path)}
                >
                    <div class="page-item-main">
                        <div class="page-item-title">
                            <Highlighted text={p.title || p.path} query={searchQuery} />
                        </div>
                        <div class="page-item-path">
                            <Highlighted text={p.path} query={searchQuery} />
                        </div>
                    </div>
                    {onRowAction && <RowActionsButton page={p} onAction={onRowAction} />}
                </li>
            ))}
        </ul>
    );
}
