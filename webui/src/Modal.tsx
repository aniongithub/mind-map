// Modal — a centered card with a scrim, used for confirmations and the
// upcoming move/select flows. Parent-controlled open/close, same as
// Popover, so the modal doesn't own any of its own state.
//
// Behavior:
//   - Pressing Escape calls onClose (unless dismissOnEscape={false}).
//   - Clicking the scrim calls onClose (unless dismissOnScrim={false}).
//     Destructive flows should set dismissOnScrim={false} so a stray
//     click on the page can't be misread as confirmation/cancellation.
//   - Focus moves into the first focusable element on open. Tab cycles
//     within the modal (cheap focus trap — see comment below).
//   - The page body's overflow is locked while a modal is open so the
//     scroll position behind the scrim doesn't shift on iOS.
//
// We don't use a portal. The modal is appended where the JSX is mounted,
// which is fine because position: fixed escapes ancestor stacking
// contexts as long as no ancestor has a transform/filter/perspective.
// If that ever bites us we can revisit.

import { useEffect, useRef } from 'preact/hooks';
import { ComponentChildren } from 'preact';

interface ModalProps {
    open: boolean;
    onClose: () => void;
    /** Modal title shown in the header. Used as aria-label too. */
    title: string;
    /** Body content. */
    children: ComponentChildren;
    /** Footer (usually buttons). */
    footer?: ComponentChildren;
    /** Default true. Set false to require an explicit Cancel button. */
    dismissOnEscape?: boolean;
    /** Default true. Set false to require an explicit Cancel button. */
    dismissOnScrim?: boolean;
    /** Optional className for variant styling. */
    class?: string;
}

export function Modal({
    open, onClose, title, children, footer,
    dismissOnEscape = true, dismissOnScrim = true,
    class: className,
}: ModalProps) {
    const surfaceRef = useRef<HTMLDivElement | null>(null);
    const lastActiveRef = useRef<HTMLElement | null>(null);

    // Focus management + body scroll lock + Escape handling — all gated
    // on `open` so a closed modal is fully inert.
    useEffect(() => {
        if (!open) return;

        // Remember what had focus so we can restore it on close.
        lastActiveRef.current = (document.activeElement as HTMLElement) ?? null;

        // Lock body scroll. Save/restore the previous value so nested
        // modals (unlikely but possible) don't leak through.
        const prevOverflow = document.body.style.overflow;
        document.body.style.overflow = 'hidden';

        // Move focus into the modal next frame so the surface is in DOM.
        const raf = requestAnimationFrame(() => {
            const first = surfaceRef.current?.querySelector<HTMLElement>(
                'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
            );
            (first ?? surfaceRef.current)?.focus();
        });

        // Escape closes (when permitted).
        const onKey = (e: KeyboardEvent) => {
            if (e.key === 'Escape' && dismissOnEscape) {
                e.stopPropagation();
                onClose();
                return;
            }
            // Cheap focus trap: when Tab would leave the modal, wrap
            // back to the first/last focusable element inside. Good
            // enough for the modals we ship; full a11y focus trapping
            // (e.g. inert siblings) would be overkill.
            if (e.key === 'Tab' && surfaceRef.current) {
                const focusables = surfaceRef.current.querySelectorAll<HTMLElement>(
                    'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
                );
                if (focusables.length === 0) return;
                const first = focusables[0];
                const last = focusables[focusables.length - 1];
                const active = document.activeElement as HTMLElement | null;
                if (!e.shiftKey && active === last) {
                    e.preventDefault();
                    first.focus();
                } else if (e.shiftKey && active === first) {
                    e.preventDefault();
                    last.focus();
                }
            }
        };
        document.addEventListener('keydown', onKey, true);

        return () => {
            cancelAnimationFrame(raf);
            document.removeEventListener('keydown', onKey, true);
            document.body.style.overflow = prevOverflow;
            // Restore focus to whatever opened the modal.
            lastActiveRef.current?.focus?.();
        };
    }, [open, dismissOnEscape, onClose]);

    if (!open) return null;

    const onScrimMouseDown = (e: MouseEvent) => {
        if (!dismissOnScrim) return;
        // Only dismiss when the press *and* release happen on the scrim
        // itself. Otherwise a click that started inside the modal and
        // ended on the scrim (drag-select / button-press-drag) would
        // also close the modal, which feels wrong.
        if (e.target === e.currentTarget) onClose();
    };

    return (
        <div
            class="modal-scrim"
            onMouseDown={onScrimMouseDown}
            role="presentation"
        >
            <div
                ref={surfaceRef}
                role="dialog"
                aria-modal="true"
                aria-label={title}
                class={`modal ${className ?? ''}`}
                tabIndex={-1}
            >
                <header class="modal-header">{title}</header>
                <div class="modal-body">{children}</div>
                {footer && <footer class="modal-footer">{footer}</footer>}
            </div>
        </div>
    );
}
