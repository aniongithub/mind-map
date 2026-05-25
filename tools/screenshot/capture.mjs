#!/usr/bin/env node
// End-to-end demo + visual test for mind-map's image-support feature.
//
// Composition model: each capture entry has an async `compose(page)`
// function that puts the SPA into the desired state — navigate,
// click controls, fill inputs, set localStorage for theme/sort, etc.
// The capture runs after compose returns, so this script can be
// extended with new shots by appending entries; the rest of the
// machinery doesn't change.
//
// Lifecycle of each capture:
//   1. DELETE /api/assets/<page>.assets/<name> — drop any prior
//      version so re-runs produce a single canonical file (no -1/-2
//      suffix accumulation).
//   2. compose(page) sets up the SPA state.
//   3. Screenshot the viewport, save to ./captured/<name>.
//   4. Upload via POST /api/assets, then GET /assets/<path> to
//      byte-verify the static handler.
//   5. After all captures, PUT each touched page once with the
//      managed sentinel block replaced.
//
// Sync: this harness mutates pages and assets via the wiki API. If
// sync is enabled with direction:pull, the next sync tick will wipe
// the edits. Either disable sync or set direction:push before
// running.

import { chromium } from 'playwright';
import { mkdir, writeFile, readFile } from 'node:fs/promises';
import { resolve, dirname, basename } from 'node:path';
import { fileURLToPath } from 'node:url';
import { Buffer } from 'node:buffer';

const __dirname = dirname(fileURLToPath(import.meta.url));
const CAPTURED_DIR = resolve(__dirname, 'captured');

const SERVER = process.env.MINDMAP_URL || 'http://localhost:51888';

// Compose helpers used across captures. Kept here rather than inside
// the entries so the entries stay readable.

async function setTheme(page, theme) {
    // Theme is persisted in localStorage and applied as a `.dark`
    // class on <html>. Setting both before navigation ensures the
    // first paint matches; flushing again post-navigation guards
    // against the app's own initialization overwriting our value
    // from prefers-color-scheme.
    await page.addInitScript((t) => {
        localStorage.setItem('mm-theme', t);
    }, theme);
}

async function setSortMode(page, mode) {
    await page.addInitScript((m) => {
        localStorage.setItem('mm-sort-mode', m);
    }, mode);
}

async function waitForMarkdown(page) {
    try {
        await page.waitForSelector('.markdown', { timeout: 5000 });
    } catch (_) {
        // Best-effort; capture whatever rendered.
    }
}

async function waitForGraph(page) {
    // The graph canvas takes a moment to lay out (force simulation).
    // Wait for the fit-all button to be reachable as a proxy for
    // "graph is up", then a beat for the animation to settle.
    try {
        await page.waitForSelector('button.graph-fit', { timeout: 5000 });
    } catch (_) { }
    await page.waitForTimeout(800);
}

async function fitGraph(page) {
    const btn = page.locator('button.graph-fit').first();
    if (await btn.isVisible().catch(() => false)) {
        await btn.click();
        await page.waitForTimeout(600); // fit animation
    }
}

async function fillSearch(page, q) {
    const input = page.locator('input[placeholder="search..."]').first();
    await input.fill(q);
    await input.press('Enter');
    await page.waitForTimeout(700);
}

// CAPTURES is the source of truth for what gets shot, where it lands,
// and how the SPA gets put into the right state.
const CAPTURES = [
    {
        name: 'home-graph-fit.png',
        embedPage: 'architecture/index',
        caption: 'Home: graph view fitted to the viewport',
        compose: async (page) => {
            await setSortMode(page, 'title');
            await setTheme(page, 'light');
            await page.goto(SERVER + '/#/', { waitUntil: 'networkidle' });
            await waitForGraph(page);
            await fitGraph(page);
        },
    },
    {
        name: 'home-graph-dark.png',
        embedPage: 'architecture/index',
        caption: 'Home: graph view in dark mode (same data, theme toggle demo)',
        compose: async (page) => {
            await setSortMode(page, 'title');
            await setTheme(page, 'dark');
            await page.goto(SERVER + '/#/', { waitUntil: 'networkidle' });
            await waitForGraph(page);
            await fitGraph(page);
        },
    },
    {
        name: 'page-detail.png',
        embedPage: 'architecture/wiki-engine',
        caption: 'Page detail: rendered markdown, wikilinks, and embedded mermaid diagrams',
        compose: async (page) => {
            await setTheme(page, 'light');
            await page.goto(SERVER + '/#/architecture/wiki-engine', { waitUntil: 'networkidle' });
            await waitForMarkdown(page);
            await page.waitForTimeout(800); // let mermaid finish
        },
    },
    {
        name: 'sort-recent.png',
        embedPage: 'architecture/web-ui',
        caption: 'Sidebar: recent-first sort (mtime-ordered list)',
        compose: async (page) => {
            await setSortMode(page, 'recent');
            await setTheme(page, 'light');
            await page.goto(SERVER + '/#/', { waitUntil: 'networkidle' });
            await waitForGraph(page);
            await fitGraph(page);
        },
    },
    {
        name: 'sort-path.png',
        embedPage: 'architecture/web-ui',
        caption: 'Sidebar: path-tree sort (hierarchical view)',
        compose: async (page) => {
            await setSortMode(page, 'path');
            await setTheme(page, 'light');
            await page.goto(SERVER + '/#/', { waitUntil: 'networkidle' });
            await waitForGraph(page);
            await fitGraph(page);
        },
    },
    {
        name: 'sort-title.png',
        embedPage: 'architecture/web-ui',
        caption: 'Sidebar: title-alphabetical sort',
        compose: async (page) => {
            await setSortMode(page, 'title');
            await setTheme(page, 'light');
            await page.goto(SERVER + '/#/', { waitUntil: 'networkidle' });
            await waitForGraph(page);
            await fitGraph(page);
        },
    },
    {
        name: 'search-sidebar.png',
        embedPage: 'architecture/mcp-server',
        caption: 'Sidebar search: results filtered + highlighted as you type',
        compose: async (page) => {
            await setSortMode(page, 'title');
            await setTheme(page, 'light');
            await page.goto(SERVER + '/#/', { waitUntil: 'networkidle' });
            await waitForGraph(page);
            await fillSearch(page, 'wiki');
        },
    },
    {
        name: 'search-in-page.png',
        embedPage: 'architecture/mcp-server',
        caption: 'In-page search: match-highlighting in the rendered body',
        compose: async (page) => {
            await setSortMode(page, 'title');
            await setTheme(page, 'light');
            // Navigate without query first, then set the query via the
            // sidebar search — initial-load hash parsing doesn't pick
            // up ?q= on first paint in all cases, but a real user-
            // initiated search always works.
            await page.goto(SERVER + '/#/architecture/wiki-engine', { waitUntil: 'networkidle' });
            await waitForMarkdown(page);
            await fillSearch(page, 'index');
            await page.waitForTimeout(400);
        },
    },
    {
        name: 'settings.png',
        embedPage: 'architecture/http-api',
        caption: 'Settings: sync configuration and direction toggle',
        compose: async (page) => {
            await setTheme(page, 'light');
            await page.goto(SERVER + '/#/', { waitUntil: 'networkidle' });
            await waitForGraph(page);
            const btn = page.locator('button.settings-toggle').first();
            await btn.click();
            await page.waitForSelector('.settings-title', { timeout: 5000 });
            await page.waitForTimeout(300);
        },
    },
];

// --- API helpers ---

async function waitForServer(attempts = 30) {
    for (let i = 0; i < attempts; i++) {
        try {
            const res = await fetch(SERVER + '/api/version');
            if (res.ok) return await res.json();
        } catch (_) { }
        await new Promise((r) => setTimeout(r, 500));
    }
    throw new Error(`server at ${SERVER} did not respond after ${attempts} attempts`);
}

async function deleteAssetIfExists(page, name) {
    // Best-effort: it's fine for the DELETE to 404 on the first run
    // when the file doesn't exist yet. We only want to clean up
    // prior versions to keep filenames canonical (home.png stays
    // home.png across runs, no -1/-2 suffix accumulation).
    const path = `${page}.assets/${name}`;
    const res = await fetch(SERVER + '/api/assets/' + path, { method: 'DELETE' });
    if (res.ok || res.status === 404) return;
    console.warn(`  warning: DELETE /api/assets/${path} returned ${res.status}`);
}

async function uploadAsset(page, name, bytes) {
    const res = await fetch(SERVER + '/api/assets', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
            page,
            name,
            content_base64: bytes.toString('base64'),
        }),
    });
    if (!res.ok) {
        throw new Error(`upload failed ${res.status}: ${await res.text()}`);
    }
    return await res.json();
}

async function getPageBody(path) {
    const res = await fetch(SERVER + '/api/pages/' + path);
    if (!res.ok) throw new Error(`get page ${path} failed ${res.status}`);
    return await res.json();
}

async function putPage(path, content) {
    const res = await fetch(SERVER + '/api/pages/' + path, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content }),
    });
    if (!res.ok) {
        throw new Error(`put page ${path} failed ${res.status}: ${await res.text()}`);
    }
    return await res.json();
}

async function verifyServed(assetPath, expected) {
    const res = await fetch(SERVER + '/assets/' + assetPath);
    if (!res.ok) throw new Error(`serve ${assetPath} failed ${res.status}`);
    const got = Buffer.from(await res.arrayBuffer());
    if (got.length !== expected.length) {
        throw new Error(`served length mismatch ${got.length} vs ${expected.length}`);
    }
    for (let i = 0; i < got.length; i++) {
        if (got[i] !== expected[i]) {
            throw new Error(`served bytes differ at offset ${i}`);
        }
    }
}

// rebuildBody overwrites the managed sentinel block on a page with a
// fresh gallery. Idempotent: re-running replaces the prior block
// rather than appending. Content outside the sentinel block is
// untouched.
function rebuildBody(originalBody, embeds) {
    const sentinelStart = '<!-- mind-map screenshots: managed; do not edit by hand -->';
    const sentinelEnd = '<!-- /mind-map screenshots -->';

    const startIdx = originalBody.indexOf(sentinelStart);
    let base = originalBody;
    if (startIdx >= 0) {
        const endIdx = originalBody.indexOf(sentinelEnd, startIdx);
        if (endIdx >= 0) {
            base = originalBody.slice(0, startIdx).trimEnd() +
                '\n' + originalBody.slice(endIdx + sentinelEnd.length);
        }
    }

    let block = '\n\n' + sentinelStart + '\n\n## Screenshots\n\n';
    for (const e of embeds) {
        block += `![${e.caption}](${e.path})\n\n*${e.caption}*\n\n`;
    }
    block += sentinelEnd + '\n';
    return base.trimEnd() + block;
}

async function main() {
    console.log('mind-map demo capture against', SERVER);

    const version = await waitForServer();
    console.log('server version:', version);

    await mkdir(CAPTURED_DIR, { recursive: true });

    const browser = await chromium.launch({
        args: ['--no-sandbox'],
    });

    // Group captures by embedPage so each page gets ONE PUT with all
    // its embeds in order. Pages can host multiple screenshots.
    const perPage = new Map();

    try {
        for (const cap of CAPTURES) {
            console.log('  capture', cap.name, '->', cap.embedPage);

            // Drop any prior version so the filename stays canonical.
            await deleteAssetIfExists(cap.embedPage, cap.name);

            // Fresh context per capture so localStorage / addInitScript
            // calls don't bleed across shots (e.g. theme changes).
            const context = await browser.newContext({
                viewport: { width: 1440, height: 900 },
                deviceScaleFactor: 2,
            });
            const page = await context.newPage();
            try {
                await cap.compose(page);
                const buf = await page.screenshot({
                    fullPage: false,
                    type: 'png',
                });

                const localPath = resolve(CAPTURED_DIR, cap.name);
                await writeFile(localPath, buf);

                const upload = await uploadAsset(cap.embedPage, cap.name, buf);
                console.log('    uploaded', upload.path, `(${buf.length} bytes)`);

                await verifyServed(upload.path, buf);

                if (!perPage.has(cap.embedPage)) perPage.set(cap.embedPage, []);
                perPage.get(cap.embedPage).push({
                    path: upload.path,
                    caption: cap.caption,
                });
            } finally {
                await page.close();
                await context.close();
            }
        }
    } finally {
        await browser.close();
    }

    for (const [pagePath, embeds] of perPage) {
        const current = await getPageBody(pagePath);
        const newBody = rebuildBody(current.body || '', embeds);
        await putPage(pagePath, newBody);
        console.log('  updated', pagePath, 'with', embeds.length, 'embed(s)');
    }

    console.log('done. captured screenshots in', CAPTURED_DIR);
}

main().catch((err) => {
    console.error('FAIL:', err.stack || err);
    process.exit(1);
});
