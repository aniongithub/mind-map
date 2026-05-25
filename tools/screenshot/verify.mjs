#!/usr/bin/env node
// Round-trip verification: open one of the pages we just modified and
// confirm the embedded screenshot actually renders (i.e. the SPA
// rewrote the markdown reference to /assets/, marked produced an
// <img>, the browser fetched the bytes from the static handler, and
// the image is visible in the rendered output).
//
// Fails loudly if anything in the pipeline broke.

import { chromium } from 'playwright';

const SERVER = process.env.MINDMAP_URL || 'http://localhost:4242';
const TARGET_PAGE = process.env.MINDMAP_TARGET || 'architecture/wiki-engine';

async function main() {
    const browser = await chromium.launch({ args: ['--no-sandbox'] });
    const ctx = await browser.newContext({
        viewport: { width: 1280, height: 800 },
        deviceScaleFactor: 2,
    });
    const page = await ctx.newPage();

    // Capture network requests so we can confirm the SPA actually
    // hit /assets/<path>.
    const assetRequests = [];
    page.on('response', (r) => {
        const u = new URL(r.url());
        if (u.pathname.startsWith('/assets/')) {
            assetRequests.push({
                path: u.pathname,
                status: r.status(),
                ct: r.headers()['content-type'] || '',
            });
        }
    });

    await page.goto(SERVER + '/#/' + TARGET_PAGE, { waitUntil: 'networkidle' });
    await page.waitForSelector('.markdown', { timeout: 5000 });
    // Give the asset request time to land.
    await page.waitForTimeout(1000);

    // The rendered HTML should now contain an <img> whose src starts
    // with /assets/. Inspect the DOM directly.
    const imgInfo = await page.evaluate(() => {
        const imgs = Array.from(document.querySelectorAll('.markdown img'));
        return imgs.map((i) => ({
            src: i.getAttribute('src'),
            naturalWidth: i.naturalWidth,
            naturalHeight: i.naturalHeight,
            complete: i.complete,
        }));
    });

    console.log('rendered images in .markdown:');
    for (const i of imgInfo) console.log(' ', i);

    console.log('asset HTTP responses:');
    for (const r of assetRequests) console.log(' ', r);

    if (imgInfo.length === 0) {
        throw new Error('no <img> rendered in .markdown — image rewrite or marked parsing failed');
    }
    const broken = imgInfo.filter((i) => !i.complete || i.naturalWidth === 0);
    if (broken.length > 0) {
        throw new Error('some images failed to load: ' + JSON.stringify(broken));
    }
    const failedRequests = assetRequests.filter((r) => r.status !== 200);
    if (failedRequests.length > 0) {
        throw new Error('asset handler returned non-200: ' + JSON.stringify(failedRequests));
    }
    if (assetRequests.length === 0) {
        throw new Error('no /assets/ requests observed — rewrite may have failed');
    }

    // Final visual confirmation: scroll the embedded image into view
    // and screenshot it so a human review can see the actual rendered
    // result. The DOM checks above are the authoritative pass/fail;
    // this is for the eyeball test.
    await page.evaluate(() => {
        const img = document.querySelector('.markdown img');
        if (img) img.scrollIntoView({ block: 'center' });
    });
    await page.waitForTimeout(300);
    await page.screenshot({ path: '/tmp/rendered-with-image.png', type: 'png' });
    console.log('OK: all images rendered. screenshot at /tmp/rendered-with-image.png');

    await browser.close();
}

main().catch((err) => {
    console.error('FAIL:', err.stack || err);
    process.exit(1);
});
