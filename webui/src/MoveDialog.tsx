// MoveDialog — pick a destination for a page (or a set of pages) and
// move them. Handles two phases in a single modal:
//
//   step="pick"     — filter bar + page/folder list + free-text new path.
//   step="confirm"  — destination already exists; confirm overwrite.
//
// Picking a *folder* moves the source(s) into that folder, keeping the
// leaf segment. Picking a *page* (or typing a path that matches one)
// is a page-replace; the user gets a second confirmation before the
// destination's content is clobbered.
//
// For multi-source moves (from select mode) only folder destinations
// are offered as click targets — moving N pages onto a single existing
// page makes no sense. The same modal still surfaces collisions per
// page when they happen.

import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { Modal } from './Modal';
import { Page, api, DestinationExistsError } from './api';

interface MoveDialogProps {
    open: boolean;
    /** Pages being moved. One for the row ⋯ menu, N for select mode. */
    sources: Page[];
    /** All pages in the wiki — used to build the destination list. */
    allPages: Page[];
    onCancel: () => void;
    /** Called after all sources have been successfully moved. */
    onDone: () => void;
}

interface DestEntry {
    /** Display label (folder name or page title). */
    label: string;
    /** The full path, what move_page sends as `to`. */
    path: string;
    type: 'folder' | 'page';
}

export function MoveDialog({ open, sources, allPages, onCancel, onDone }: MoveDialogProps) {
    const multi = sources.length > 1;
    const [filter, setFilter] = useState('');
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState<string | null>(null);
    // When non-null, we're in confirm-overwrite step: { to } is the
    // resolved destination path that collided.
    const [overwrite, setOverwrite] = useState<{ to: string } | null>(null);
    const filterRef = useRef<HTMLInputElement | null>(null);

    // Reset every time the dialog opens with a fresh set.
    useEffect(() => {
        if (open) {
            setFilter('');
            setOverwrite(null);
            setError(null);
            // Focus the filter input shortly after the modal mounts so
            // the user can start typing immediately.
            requestAnimationFrame(() => filterRef.current?.focus());
        }
    }, [open, sources]);

    // Build folder + page lists from allPages. Folders are derived from
    // the unique prefix of every page path. Source paths are excluded —
    // moving a page onto itself is a no-op the engine would reject.
    //
    // The wiki root is included as a special "<root>" folder entry —
    // without it, single-segment pages (e.g. "index") would have no
    // folder destinations at all, and there'd be no way to move a
    // page *out* of a subdirectory back to the top level. The entry's
    // path is the empty string; toPathFor handles that as a no-prefix
    // destination.
    const sourcePaths = useMemo(() => new Set(sources.map(s => s.path)), [sources]);
    const allEntries = useMemo<DestEntry[]>(() => {
        const folders = new Set<string>();
        for (const p of allPages) {
            const parts = p.path.split('/');
            for (let i = 1; i < parts.length; i++) {
                folders.add(parts.slice(0, i).join('/'));
            }
        }
        const out: DestEntry[] = [];
        // Wiki root always comes first.
        out.push({ label: '<root>', path: '', type: 'folder' });
        // Then real folders (alphabetically), then pages.
        for (const f of [...folders].sort()) {
            out.push({ label: f, path: f, type: 'folder' });
        }
        for (const p of [...allPages].sort((a, b) => a.path.localeCompare(b.path))) {
            if (sourcePaths.has(p.path)) continue;
            // In multi-source mode we still list pages for visibility
            // ("X exists") but clicking a page in multi mode is
            // ambiguous, so we'll disable that interaction below.
            out.push({ label: p.title || p.path, path: p.path, type: 'page' });
        }
        return out;
    }, [allPages, sourcePaths]);

    // Filter substring-match against the path (and label, so the
    // <root> entry is reachable by typing "root"). Case-insensitive.
    const filtered = useMemo(() => {
        if (!filter.trim()) return allEntries.slice(0, 50);
        const q = filter.toLowerCase();
        return allEntries.filter(e =>
            e.path.toLowerCase().includes(q) || e.label.toLowerCase().includes(q)
        ).slice(0, 50);
    }, [allEntries, filter]);

    // If the user typed a path that doesn't exist as either a folder or
    // a page, surface a "Move to: <typed-path>" affordance. Trim a
    // leading slash so people can paste with or without one.
    const typed = filter.trim().replace(/^\/+/, '');
    const typedIsValid = typed.length > 0 && !typed.endsWith('/') && !typed.includes('//');
    const typedMatchesExisting = typedIsValid && allEntries.some(e => e.path === typed);
    const showCreateOption = typedIsValid && !typedMatchesExisting;

    /**
     * Resolve the actual `to` path for a given destination, given the
     * sources. Folder destinations append each source's leaf segment;
     * page destinations replace the page directly. New-path entries
     * (from the typed-create affordance) are treated as folders when
     * the input ends in something that already exists as a folder, or
     * as a page-path otherwise. We keep this simple by always treating
     * the typed input as a literal destination *unless* it's exactly a
     * known folder, in which case we append the leaf — that matches
     * the user's intuition when typing a path that happens to be a
     * folder name they remember.
     */
    const toPathFor = (dest: DestEntry, source: Page): string => {
        if (dest.type === 'folder') {
            const leaf = source.path.split('/').pop() ?? source.path;
            // The wiki root has an empty path; concatenating with '/' would
            // produce '/leaf'. Strip that to just 'leaf'.
            return dest.path === '' ? leaf : `${dest.path}/${leaf}`;
        }
        return dest.path;
    };
    const typedToPathFor = (typedPath: string, source: Page): string => {
        // If typedPath exactly matches an existing folder, treat as
        // folder-move. Otherwise treat as a literal destination.
        const isExistingFolder = allEntries.some(e => e.path === typedPath && e.type === 'folder');
        if (isExistingFolder) {
            const leaf = source.path.split('/').pop() ?? source.path;
            return `${typedPath}/${leaf}`;
        }
        return typedPath;
    };

    /**
     * Issue the moves. `dest` may be a DestEntry (clicked) or a literal
     * path string (typed). On the first collision we stop, surface the
     * confirm-overwrite step, and let the user retry from there.
     */
    const doMove = async (resolveTo: (s: Page) => string, withOverwrite: boolean) => {
        setBusy(true);
        setError(null);
        try {
            for (const s of sources) {
                const to = resolveTo(s);
                try {
                    await api.movePage(s.path, to, withOverwrite);
                } catch (e) {
                    if (e instanceof DestinationExistsError) {
                        // Pause: ask the user. Store the resolved `to`
                        // so the next call uses the same one with
                        // overwrite=true. We don't try to be clever
                        // about batching multiple collisions — each
                        // collision is its own confirmation.
                        setOverwrite({ to: e.to });
                        // Stash the in-progress info on the closure
                        // for the retry handler to pick up.
                        pendingRef.current = { resolveTo, remaining: sources.slice(sources.indexOf(s)) };
                        setBusy(false);
                        return;
                    }
                    throw e;
                }
            }
            setBusy(false);
            onDone();
        } catch (e) {
            console.error('move failed:', e);
            setError(e instanceof Error ? e.message : String(e));
            setBusy(false);
        }
    };

    // Holds state across the pause for overwrite confirmation. A ref
    // (not state) so resuming doesn't re-render mid-flight.
    const pendingRef = useRef<{
        resolveTo: (s: Page) => string;
        remaining: Page[];
    } | null>(null);

    const confirmOverwrite = async () => {
        const pending = pendingRef.current;
        if (!pending) return;
        setBusy(true);
        setError(null);
        setOverwrite(null);
        try {
            // First retry the colliding one with overwrite=true, then
            // continue the rest with overwrite=false (collisions on
            // subsequent ones get their own prompt).
            const [first, ...rest] = pending.remaining;
            await api.movePage(first.path, pending.resolveTo(first), true);
            for (const s of rest) {
                const to = pending.resolveTo(s);
                try {
                    await api.movePage(s.path, to, false);
                } catch (e) {
                    if (e instanceof DestinationExistsError) {
                        setOverwrite({ to: e.to });
                        pendingRef.current = { resolveTo: pending.resolveTo, remaining: pending.remaining.slice(pending.remaining.indexOf(s)) };
                        setBusy(false);
                        return;
                    }
                    throw e;
                }
            }
            pendingRef.current = null;
            setBusy(false);
            onDone();
        } catch (e) {
            console.error('overwrite move failed:', e);
            setError(e instanceof Error ? e.message : String(e));
            setBusy(false);
        }
    };

    const cancelOverwrite = () => {
        setOverwrite(null);
        pendingRef.current = null;
    };

    // --- Render: confirm-overwrite step ----------------------------------

    if (overwrite) {
        return (
            <Modal
                open={open}
                onClose={busy ? () => {} : cancelOverwrite}
                title="Replace existing page?"
                dismissOnScrim={false}
                class="destructive"
                footer={
                    <>
                        <button class="btn" type="button" onClick={cancelOverwrite} disabled={busy}>
                            Cancel
                        </button>
                        <button class="btn primary" type="button" onClick={confirmOverwrite} disabled={busy}>
                            {busy ? 'Replacing…' : 'Replace'}
                        </button>
                    </>
                }
            >
                <div><strong>"{overwrite.to}" already exists.</strong></div>
                <div style="margin-top: 6px; color: var(--fg-muted);">
                    Continuing will replace it. Its content will be lost.
                </div>
            </Modal>
        );
    }

    // --- Render: pick-destination step -----------------------------------

    const heading = multi
        ? `Move ${sources.length} pages to…`
        : `Move "${sources[0]?.title || sources[0]?.path}" to…`;

    return (
        <Modal
            open={open}
            onClose={busy ? () => {} : onCancel}
            title="Move"
            footer={
                <>
                    <button class="btn" type="button" onClick={onCancel} disabled={busy}>
                        Cancel
                    </button>
                </>
            }
        >
            <div style="margin-bottom: 8px;"><strong>{heading}</strong></div>
            {multi && (
                <div style="margin-bottom: 8px; color: var(--fg-muted); font-size: 12.5px;">
                    Pick a folder. Each page keeps its current name.
                </div>
            )}
            <input
                ref={filterRef}
                type="text"
                class="move-filter"
                placeholder="Type a path or filter…"
                value={filter}
                onInput={(e) => setFilter((e.target as HTMLInputElement).value)}
                disabled={busy}
            />
            {error && <div class="move-error">{error}</div>}
            <ul class="move-list" role="listbox" aria-label="Destinations">
                {showCreateOption && (
                    <li>
                        <button
                            type="button"
                            class="move-item move-item-create"
                            role="option"
                            disabled={busy}
                            onClick={() => doMove(s => typedToPathFor(typed, s), false)}
                        >
                            <span class="move-item-icon" aria-hidden="true">＋</span>
                            <span class="move-item-main">
                                <span class="move-item-label">Move to: {typed}</span>
                                <span class="move-item-sub">create new path</span>
                            </span>
                        </button>
                    </li>
                )}
                {filtered.map(e => {
                    // Multi-source moves disable page targets — only
                    // folders are meaningful destinations for N pages.
                    const disabledForMulti = multi && e.type === 'page';
                    const isRoot = e.type === 'folder' && e.path === '';
                    return (
                        <li key={`${e.type}:${e.path}`}>
                            <button
                                type="button"
                                class={`move-item ${e.type === 'folder' ? 'is-folder' : 'is-page'} ${isRoot ? 'is-root' : ''}`}
                                role="option"
                                disabled={busy || disabledForMulti}
                                title={
                                    disabledForMulti
                                        ? 'Cannot move multiple pages onto a single page'
                                        : isRoot
                                            ? 'Wiki root (top level)'
                                            : e.path
                                }
                                onClick={() => doMove(s => toPathFor(e, s), false)}
                            >
                                <span class="move-item-icon" aria-hidden="true">
                                    {isRoot ? '🏠' : e.type === 'folder' ? '📁' : '📄'}
                                </span>
                                <span class="move-item-main">
                                    <span class="move-item-label">{e.label}</span>
                                    <span class="move-item-sub">
                                        {isRoot ? 'top level of the wiki' : e.path}
                                    </span>
                                </span>
                            </button>
                        </li>
                    );
                })}
                {filtered.length === 0 && !showCreateOption && (
                    <li class="move-empty">No matches. Type a path to create a new one.</li>
                )}
            </ul>
        </Modal>
    );
}
