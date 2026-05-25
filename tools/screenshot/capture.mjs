#!/usr/bin/env node
// End-to-end visual test for mind-map's image-support feature.
//
// Flow:
//   1. Connect to a running mind-map server (default http://localhost:4242).
//   2. For each view in CAPTURES, navigate the browser, wait for content,
//      and take a full-page screenshot.
//   3. Save each PNG to ./captured/ so a human can diff or attach to a PR.
//   4. Upload each PNG via POST /api/assets to the configured page.
//   5. PUT the page with the embed reference appended.
//   6. Verify by GETting /assets/<path> and checking the bytes match.
//
// This exercises the whole pipeline end-to-end: upload tool ->
// sidecar storage -> link indexing -> static handler -> marked
// rendering in the SPA. If any of the slices regressed, this script
// fails with a concrete error pointing at the broken seam.

import { chromium } from 'playwright';
import { mkdir, writeFile, readFile } from 'node:fs/promises';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { Buffer } from 'node:buffer';

const __dirname = dirname(fileURLToPath(import.meta.url));
const CAPTURED_DIR = resolve(__dirname, 'captured');

const SERVER = process.env.MINDMAP_URL || 'http://localhost:4242';

// Each capture describes one screenshot. `page` is the wiki page it
// gets embedded into; the asset name keeps a stable filename across
// runs so the page body's reference stays valid.
// Each capture describes one screenshot. `setup` is an async function
// that puts the page into the desired state (navigate, click, fill
// search, etc.); `embedPage` is the wiki page that receives the
// rendered <img> reference. Stable filenames mean re-runs replace the
// existing screenshot instead of accumulating duplicates.
const CAPTURES = [
    {
        name: 'home.png',
        embedPage: 'architecture/index',
        caption: 'Home view (graph + sidebar)',
        setup: async (page) => {
            await page.goto(SERVER + '/#/', { waitUntil: 'networkidle' });
            await page.waitForTimeout(800);
        },
    },
    {
        name: 'page-detail.png',
        embedPage: 'architecture/wiki-engine',
        caption: 'Page detail with rendered markdown and wikilinks',
        setup: async (page) => {
            await page.goto(SERVER + '/#/architecture/wiki-engine', { waitUntil: 'networkidle' });
            try {
                await page.waitForSelector('.markdown', { timeout: 5000 });
            } catch (_) {/* render anyway */ }
            await page.waitForTimeout(500);
        },
    },
    {
        name: 'search.png',
        embedPage: 'architecture/mcp-server',
        caption: 'Full-text search across the wiki',
        setup: async (page) => {
            await page.goto(SERVER + '/#/', { waitUntil: 'networkidle' });
            await page.waitForTimeout(500);
            // The sidebar search input is a placeholder="search..." text input.
            const input = page.locator('input[placeholder="search..."]').first();
            await input.fill('wiki');
            await input.press('Enter');
            await page.waitForTimeout(800);
        },
    },
    {
        name: 'mcp-page.png',
        embedPage: 'architecture/web-ui',
        caption: 'MCP server page with embedded code blocks',
        setup: async (page) => {
            await page.goto(SERVER + '/#/architecture/mcp-server', { waitUntil: 'networkidle' });
            try {
                await page.waitForSelector('.markdown', { timeout: 5000 });
            } catch (_) { }
            await page.waitForTimeout(500);
        },
    },
    {
        name: 'settings.png',
        embedPage: 'architecture/http-api',
        caption: 'Settings modal (sync configuration)',
        setup: async (page) => {
            await page.goto(SERVER + '/#/', { waitUntil: 'networkidle' });
            await page.waitForTimeout(500);
            const btn = page.locator('button.settings-toggle').first();
            await btn.click();
            await page.waitForSelector('.settings-title', { timeout: 5000 });
            await page.waitForTimeout(300);
        },
    },
];

async function waitForServer(url, attempts = 30) {
    for (let i = 0; i < attempts; i++) {
        try {
            const res = await fetch(url + '/api/version');
            if (res.ok) {
                return await res.json();
            }
        } catch (_) { /* not up yet */ }
        await new Promise((r) => setTimeout(r, 500));
    }
    throw new Error(`server at ${url} did not respond after ${attempts} attempts`);
}

async function uploadAsset(page, name, bytes) {
    const body = JSON.stringify({
        page,
        name,
        content_base64: bytes.toString('base64'),
    });
    const res = await fetch(SERVER + '/api/assets', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body,
    });
    if (!res.ok) {
        throw new Error(`upload failed ${res.status}: ${await res.text()}`);
    }
    return await res.json();
}

async function getPageBody(path) {
    const res = await fetch(SERVER + '/api/pages/' + path);
    if (!res.ok) {
        throw new Error(`get page ${path} failed ${res.status}`);
    }
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
    if (!res.ok) {
        throw new Error(`serve ${assetPath} failed ${res.status}`);
    }
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

// rebuildBody is the idempotent way to embed all captures into a page.
// We replace a managed sentinel block so re-running the script
// overwrites the existing references instead of appending duplicates.
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
    console.log('mind-map screenshot test against', SERVER);

    const version = await waitForServer(SERVER);
    console.log('server version:', version);

    await mkdir(CAPTURED_DIR, { recursive: true });

    const browser = await chromium.launch({
        // --no-sandbox is required in the devcontainer (we run as a
        // non-root user without /proc/sys/user/max_user_namespaces).
        args: ['--no-sandbox'],
    });
    const context = await browser.newContext({
        viewport: { width: 1280, height: 800 },
        deviceScaleFactor: 2, // sharper screenshots
    });

    // Group captures by embedPage so each page is updated once with
    // all of its captures in one PUT.
    const perPage = new Map();

    try {
        for (const cap of CAPTURES) {
            console.log('  capture', cap.name);
            const page = await context.newPage();
            try {
                await cap.setup(page);
                const buf = await page.screenshot({
                    fullPage: false, // viewport-sized; full-page tends to be huge
                    type: 'png',
                });

                const localPath = resolve(CAPTURED_DIR, cap.name);
                await writeFile(localPath, buf);

                const upload = await uploadAsset(cap.embedPage, cap.name, buf);
                console.log('    uploaded to', upload.path);

                // Verify the static handler serves the bytes back
                // identically before we touch the page body.
                await verifyServed(upload.path, buf);

                if (!perPage.has(cap.embedPage)) perPage.set(cap.embedPage, []);
                perPage.get(cap.embedPage).push({
                    path: upload.path,
                    caption: cap.caption,
                });
            } finally {
                await page.close();
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
