import { useState, useRef } from 'preact/hooks';

interface TagInputProps {
    value: string[];
    onChange: (next: string[]) => void;
    placeholder?: string;
    // Maximum number of tags. When reached, further input is blocked
    // until the user removes a tag. Omitted = no limit.
    maxTags?: number;
}

// TagInput is a controlled "chips + textbox" control: type a word,
// hit comma, space, or Enter, and it becomes a tag. Backspace on an
// empty input deletes the previous tag (standard chip-input UX —
// matches Gmail's To: line, GitHub's labels, etc.). Pasting a
// comma- or whitespace-separated string creates multiple tags in
// one shot.
//
// Values are de-duplicated case-insensitively but preserved in the
// case the user typed — we don't want to fold "JWT" into "jwt" on
// the way back to the server. The consumer of the values (the cloud
// builder) is the one that case-folds for matching; storing the
// user's intent verbatim respects what they typed.
export function TagInput({ value, onChange, placeholder, maxTags }: TagInputProps) {
    const [draft, setDraft] = useState('');
    const inputRef = useRef<HTMLInputElement | null>(null);

    const commit = (raw: string) => {
        // Split on commas and whitespace so pasting a list works
        // even if the user pasted "TODO, FIXME see" (mixed
        // separators). Empty fragments are filtered out by trim.
        const fragments = raw
            .split(/[\s,]+/)
            .map(s => s.trim())
            .filter(Boolean);
        if (fragments.length === 0) return;

        const lowerExisting = new Set(value.map(v => v.toLowerCase()));
        const additions: string[] = [];
        for (const f of fragments) {
            if (lowerExisting.has(f.toLowerCase())) continue;
            if (maxTags && value.length + additions.length >= maxTags) break;
            lowerExisting.add(f.toLowerCase());
            additions.push(f);
        }
        if (additions.length > 0) onChange([...value, ...additions]);
        setDraft('');
    };

    const removeAt = (idx: number) => {
        const next = value.slice();
        next.splice(idx, 1);
        onChange(next);
        // Keep focus on the input so the user can keep editing.
        inputRef.current?.focus();
    };

    const onKeyDown = (e: KeyboardEvent) => {
        // Commit triggers: Enter, comma, space. Comma and space need
        // to be intercepted so they don't actually land in the input.
        if (e.key === 'Enter' || e.key === ',' || e.key === ' ') {
            // Don't commit on a leading space inside an in-progress
            // word — user might be pasting and the paste handler
            // will fire separately. Specifically: only commit when
            // there's something to commit.
            if (draft.trim() !== '') {
                e.preventDefault();
                commit(draft);
            } else if (e.key === ',' || e.key === ' ') {
                // Swallow stray separators on an empty input so the
                // box doesn't fill with whitespace.
                e.preventDefault();
            }
            return;
        }
        if (e.key === 'Backspace' && draft === '' && value.length > 0) {
            e.preventDefault();
            removeAt(value.length - 1);
        }
    };

    const onPaste = (e: ClipboardEvent) => {
        const pasted = e.clipboardData?.getData('text') ?? '';
        if (/[\s,]/.test(pasted)) {
            // The paste contains separators — handle the whole
            // string as tags rather than letting it land in the
            // input field where the user would have to manually
            // split it.
            e.preventDefault();
            commit(pasted);
        }
    };

    return (
        <div class="tag-input" onClick={() => inputRef.current?.focus()}>
            {value.map((tag, idx) => (
                <span key={`${tag}-${idx}`} class="tag">
                    <span class="tag-label">{tag}</span>
                    <button
                        type="button"
                        class="tag-remove"
                        aria-label={`Remove ${tag}`}
                        onClick={(e) => {
                            e.stopPropagation();
                            removeAt(idx);
                        }}
                    >
                        ×
                    </button>
                </span>
            ))}
            <input
                ref={inputRef}
                type="text"
                class="tag-input-field"
                value={draft}
                placeholder={value.length === 0 ? placeholder : ''}
                onInput={(e) => setDraft((e.target as HTMLInputElement).value)}
                onKeyDown={onKeyDown}
                onPaste={onPaste}
                onBlur={() => {
                    // Commit any in-progress draft on blur so the user
                    // doesn't have to remember the keyboard ritual when
                    // they tab away or click Save.
                    if (draft.trim() !== '') commit(draft);
                }}
            />
        </div>
    );
}
