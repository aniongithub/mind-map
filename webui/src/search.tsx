import { Page } from './api';

// Tokenize a free-form search query the same way the FTS index does:
//   - "quoted phrases" become a single token (so they highlight as a
//     phrase and pass through to FTS5 as a phrase match)
//   - bare runs of non-whitespace become individual tokens
//   - leading/trailing punctuation on bare tokens is stripped
//   - empty tokens are dropped
export function searchTokens(query: string): string[] {
    const tokens: string[] = [];
    const re = /"([^"]+)"|(\S+)/g;
    let m: RegExpExecArray | null;
    while ((m = re.exec(query)) !== null) {
        const tok = m[1] !== undefined
            ? m[1].trim()
            : m[2].replace(/^[^\p{L}\p{N}_]+|[^\p{L}\p{N}_]+$/gu, '');
        if (tok) tokens.push(tok);
    }
    return tokens;
}

export function searchRegex(tokens: string[]): RegExp | null {
    if (tokens.length === 0) return null;
    // Escape regex metacharacters, then collapse interior whitespace in
    // phrase tokens to \s+ so "MCP server" still matches even if the
    // rendered text has a newline or extra spaces between the words.
    const escaped = tokens.map(t =>
        t.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
            .replace(/\s+/g, '\\s+')
    );
    return new RegExp(`(${escaped.join('|')})`, 'giu');
}

// Renders plain text with each search-query token wrapped in <mark>.
// For places that render user-supplied text directly (sidebar items,
// page header). The body uses highlightHTML (in App.tsx) instead
// because it needs to highlight inside marked-rendered HTML.
export function Highlighted({ text, query }: { text: string; query: string }) {
    const re = searchRegex(searchTokens(query));
    if (!re || !text) return <>{text}</>;
    const parts: (string | { mark: string })[] = [];
    let last = 0;
    let m: RegExpExecArray | null;
    while ((m = re.exec(text)) !== null) {
        if (m.index > last) parts.push(text.slice(last, m.index));
        parts.push({ mark: m[0] });
        last = m.index + m[0].length;
    }
    if (last < text.length) parts.push(text.slice(last));
    return <>{parts.map((p, i) => typeof p === 'string' ? p : <mark key={i}>{p.mark}</mark>)}</>;
}

export type { Page };
