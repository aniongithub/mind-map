import { Page } from './api';
import { Highlighted } from './search';
import { RowAction, RowActionsButton } from './RowActions';

interface PageListProps {
    pages: Page[];
    searchQuery: string;
    currentPath?: string;
    onNavigate: (path: string) => void;
    onRowAction?: (action: RowAction, page: Page) => void;
    /** When true, rows show a checkbox and clicking toggles selection. */
    selectMode?: boolean;
    /** Selected page paths. Only consulted when selectMode is true. */
    selected?: Set<string>;
    /** Toggle a row's selection. */
    onToggleSelect?: (path: string) => void;
}

// Flat list view. Used by the "Recent" and "A→Z title" sort modes.
export function PageList({
    pages, searchQuery, currentPath, onNavigate, onRowAction,
    selectMode, selected, onToggleSelect,
}: PageListProps) {
    return (
        <ul class={`page-list ${selectMode ? 'select-mode' : ''}`}>
            {pages.map(p => {
                const isSelected = selectMode && selected?.has(p.path);
                const handleClick = () => {
                    if (selectMode) onToggleSelect?.(p.path);
                    else onNavigate(p.path);
                };
                return (
                    <li
                        key={p.path}
                        class={`page-item ${currentPath === p.path ? 'active' : ''} ${isSelected ? 'selected' : ''}`}
                        onClick={handleClick}
                    >
                        {selectMode && (
                            <input
                                type="checkbox"
                                class="row-checkbox"
                                checked={!!isSelected}
                                // The row's onClick already toggles;
                                // stop propagation so checkbox clicks
                                // don't double-toggle.
                                onClick={(e) => e.stopPropagation()}
                                onChange={() => onToggleSelect?.(p.path)}
                                aria-label={`Select ${p.title || p.path}`}
                            />
                        )}
                        <div class="page-item-main">
                            <div class="page-item-title">
                                <Highlighted text={p.title || p.path} query={searchQuery} />
                            </div>
                            <div class="page-item-path">
                                <Highlighted text={p.path} query={searchQuery} />
                            </div>
                        </div>
                        {!selectMode && onRowAction && <RowActionsButton page={p} onAction={onRowAction} />}
                    </li>
                );
            })}
        </ul>
    );
}
