// Popover — a tiny flyout primitive used by row-level overflow menus.
//
// Why hand-rolled (vs a library):
//   - The rest of the webui has zero UI deps. Keeping that property.
//   - The menu surface area is small: anchor under a button, dismiss on
//     outside click + Escape, role="menu" for screen readers, focus
//     handling for keyboard users. All of that fits in ~80 lines.
//
// The primitive intentionally does *not* manage open/closed state — the
// parent does. That keeps the trigger behavior (button vs row vs long
// press) flexible without baking assumptions in here.
//
// Positioning is "best-effort, viewport-aware": opens below the anchor by
// default; flips above if there isn't room. We don't try to be a full
// floating-ui — long path-tree items don't need it, and flipping covers
// the only problematic case (last row clipped at sidebar bottom).

import { useEffect, useLayoutEffect, useRef, useState } from 'preact/hooks';
import { ComponentChildren, JSX } from 'preact';

interface PopoverProps {
    /** Element to anchor the flyout against (the trigger). */
    anchor: HTMLElement | null;
    /** Whether the popover is currently visible. Parent-controlled. */
    open: boolean;
    /** Called whenever the popover wants to close (outside click, Escape). */
    onClose: () => void;
    /** Menu items / arbitrary content. */
    children: ComponentChildren;
    /** ARIA role for the surface. Defaults to "menu". */
    role?: JSX.HTMLAttributes['role'];
    /** Optional className for tests / theming. */
    class?: string;
}

export function Popover({ anchor, open, onClose, children, role = 'menu', class: className }: PopoverProps) {
    const surfaceRef = useRef<HTMLDivElement | null>(null);
    const [pos, setPos] = useState<{ top: number; left: number; placement: 'below' | 'above' } | null>(null);

    // Compute position once on open and whenever the viewport changes. We
    // use useLayoutEffect so the first paint already has correct coords
    // (no visible jump).
    useLayoutEffect(() => {
        if (!open || !anchor) {
            setPos(null);
            return;
        }
        const place = () => {
            const a = anchor.getBoundingClientRect();
            const surface = surfaceRef.current;
            const surfaceH = surface?.offsetHeight ?? 0;
            const surfaceW = surface?.offsetWidth ?? 0;
            const vh = window.innerHeight;
            const vw = window.innerWidth;
            const margin = 4;

            // Default: drop below, align right edge of surface with right
            // edge of anchor (so a ⋯ button in the sidebar opens leftward
            // rather than spilling off-screen).
            let top = a.bottom + margin;
            let placement: 'below' | 'above' = 'below';
            const roomBelow = vh - a.bottom;
            if (roomBelow < surfaceH + margin && a.top > surfaceH + margin) {
                top = a.top - surfaceH - margin;
                placement = 'above';
            }
            let left = a.right - surfaceW;
            if (left < margin) left = margin;
            if (left + surfaceW > vw - margin) left = vw - margin - surfaceW;
            setPos({ top, left, placement });
        };
        place();
        // Re-place if window resizes or user scrolls — cheap to rerun.
        window.addEventListener('resize', place);
        window.addEventListener('scroll', place, true);
        return () => {
            window.removeEventListener('resize', place);
            window.removeEventListener('scroll', place, true);
        };
    }, [open, anchor]);

    // Outside-click + Escape to close. Pointerdown (not click) so we
    // dismiss even if the user mousedowns elsewhere and drags — feels
    // snappier and matches OS menu behavior.
    useEffect(() => {
        if (!open) return;
        const onPointerDown = (e: PointerEvent) => {
            const surface = surfaceRef.current;
            if (!surface) return;
            const target = e.target as Node;
            if (surface.contains(target)) return;
            if (anchor && anchor.contains(target)) return;
            onClose();
        };
        const onKey = (e: KeyboardEvent) => {
            if (e.key === 'Escape') {
                e.stopPropagation();
                onClose();
            }
        };
        document.addEventListener('pointerdown', onPointerDown, true);
        document.addEventListener('keydown', onKey, true);
        return () => {
            document.removeEventListener('pointerdown', onPointerDown, true);
            document.removeEventListener('keydown', onKey, true);
        };
    }, [open, anchor, onClose]);

    // Move focus into the surface when it opens so keyboard users can
    // immediately arrow-key through items. Defer to next frame so the
    // browser has the surface laid out.
    useEffect(() => {
        if (!open) return;
        const id = requestAnimationFrame(() => {
            const first = surfaceRef.current?.querySelector<HTMLElement>('[role="menuitem"]');
            first?.focus();
        });
        return () => cancelAnimationFrame(id);
    }, [open]);

    if (!open) return null;

    const style: JSX.CSSProperties = pos
        ? { top: `${pos.top}px`, left: `${pos.left}px`, visibility: 'visible' }
        // First render before measurement: keep off-screen so we never
        // flash an unpositioned surface.
        : { top: '-9999px', left: '-9999px', visibility: 'hidden' };

    return (
        <div
            ref={surfaceRef}
            role={role}
            class={`popover-surface ${className ?? ''}`}
            style={style}
        >
            {children}
        </div>
    );
}

interface PopoverItemProps {
    onSelect: () => void;
    children: ComponentChildren;
    /** Render as destructive (red) — used for "Delete". */
    destructive?: boolean;
    /** Optional leading icon as a string (emoji or single char). */
    icon?: string;
}

export function PopoverItem({ onSelect, children, destructive, icon }: PopoverItemProps) {
    return (
        <button
            type="button"
            role="menuitem"
            class={`popover-item ${destructive ? 'destructive' : ''}`}
            onClick={(e) => {
                e.stopPropagation();
                onSelect();
            }}
        >
            {icon && <span class="popover-item-icon" aria-hidden="true">{icon}</span>}
            <span class="popover-item-label">{children}</span>
        </button>
    );
}
