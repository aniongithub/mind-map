// DeleteConfirm — confirmation modal for single or bulk page deletion.
//
// Used by both the row ⋯ menu (single page) and the upcoming bulk-select
// toolbar (N pages). The behavior is identical: same modal, same prose,
// same red primary button. Only the path list adapts to the count.

import { Modal } from './Modal';
import { Page } from './api';

interface DeleteConfirmProps {
    open: boolean;
    pages: Page[];
    onCancel: () => void;
    /** Called after the user confirms. The parent issues the deletes. */
    onConfirm: () => void;
    /** True while the parent is mid-delete; disables the buttons. */
    busy?: boolean;
}

const MAX_VISIBLE = 5;

export function DeleteConfirm({ open, pages, onCancel, onConfirm, busy }: DeleteConfirmProps) {
    const n = pages.length;
    const visible = pages.slice(0, MAX_VISIBLE);
    const hidden = n - visible.length;

    const title = n === 1 ? 'Delete page?' : `Delete ${n} pages?`;
    const heading =
        n === 1
            ? `Delete "${pages[0].title || pages[0].path}"?`
            : `Delete ${n} pages?`;

    return (
        <Modal
            open={open}
            onClose={busy ? () => { /* ignore close while busy */ } : onCancel}
            title={title}
            // Destructive: require explicit Cancel; no scrim-dismiss.
            dismissOnScrim={false}
            class="destructive"
            footer={
                <>
                    <button class="btn" type="button" onClick={onCancel} disabled={busy}>
                        Cancel
                    </button>
                    <button class="btn primary" type="button" onClick={onConfirm} disabled={busy}>
                        {busy ? 'Deleting…' : 'Delete'}
                    </button>
                </>
            }
        >
            <div><strong>{heading}</strong></div>
            <div style="margin-top: 6px; color: var(--fg-muted);">
                This cannot be undone.
            </div>
            {n > 0 && (
                <>
                    <ul class="modal-path-list" aria-label="Pages to delete">
                        {visible.map(p => (
                            <li key={p.path}>{p.path}</li>
                        ))}
                    </ul>
                    {hidden > 0 && (
                        <div class="modal-path-list-more">
                            …and {hidden} more
                        </div>
                    )}
                </>
            )}
        </Modal>
    );
}
