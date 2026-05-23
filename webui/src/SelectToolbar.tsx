// SelectToolbar — appears above the page list while in multi-select
// mode. Shows the selection count and the bulk-action affordances.
//
// The toolbar reuses the same action vocabulary as the row ⋯ menu so
// the same modals (DeleteConfirm, MoveDialog) handle both flows.

interface SelectToolbarProps {
    count: number;
    onMove: () => void;
    onDelete: () => void;
    onCancel: () => void;
}

export function SelectToolbar({ count, onMove, onDelete, onCancel }: SelectToolbarProps) {
    return (
        <div class="select-toolbar" role="toolbar" aria-label="Bulk actions">
            <div class="select-count" aria-live="polite">
                {count} selected
            </div>
            <div class="select-actions">
                <button
                    type="button"
                    class="select-action"
                    onClick={onMove}
                    disabled={count === 0}
                    title="Move selected pages"
                >
                    Move…
                </button>
                <button
                    type="button"
                    class="select-action destructive"
                    onClick={onDelete}
                    disabled={count === 0}
                    title="Delete selected pages"
                >
                    Delete…
                </button>
                <button
                    type="button"
                    class="select-action"
                    onClick={onCancel}
                    title="Exit select mode"
                >
                    Cancel
                </button>
            </div>
        </div>
    );
}
