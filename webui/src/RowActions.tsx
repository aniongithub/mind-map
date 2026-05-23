// RowActions — the ⋯ overflow trigger and its three-item menu used on
// every page row in the sidebar tree and flat list.
//
// The button is the sole visible element until the menu opens. We keep
// it dumb: the menu items emit an `action` string and let the parent
// decide what to do with it. That way the bulk-select toolbar can reuse
// the exact same action vocabulary later.

import { useRef, useState } from 'preact/hooks';
import { Popover, PopoverItem } from './Popover';
import { Page } from './api';

export type RowAction = 'select' | 'move' | 'delete';

interface RowActionsButtonProps {
    page: Page;
    onAction: (action: RowAction, page: Page) => void;
}

export function RowActionsButton({ page, onAction }: RowActionsButtonProps) {
    const [open, setOpen] = useState(false);
    const btnRef = useRef<HTMLButtonElement | null>(null);

    const handle = (action: RowAction) => {
        setOpen(false);
        onAction(action, page);
    };

    return (
        <>
            <button
                ref={btnRef}
                type="button"
                class="row-overflow-btn"
                aria-haspopup="menu"
                aria-expanded={open}
                aria-label={`Actions for ${page.title || page.path}`}
                title="More actions"
                onClick={(e) => {
                    // Don't let the click bubble into the row's
                    // navigation handler.
                    e.stopPropagation();
                    setOpen(v => !v);
                }}
            >
                {/* Three centered dots — same glyph as macOS / GitHub
                  * overflow menus. Using the character avoids an SVG
                  * dependency. */}
                &#x22EF;
            </button>
            <Popover anchor={btnRef.current} open={open} onClose={() => setOpen(false)}>
                <PopoverItem icon="☐" onSelect={() => handle('select')}>Select</PopoverItem>
                <PopoverItem icon="→" onSelect={() => handle('move')}>Move…</PopoverItem>
                <PopoverItem icon="✕" destructive onSelect={() => handle('delete')}>Delete…</PopoverItem>
            </Popover>
        </>
    );
}
