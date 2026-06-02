/**
 * Playwright script to capture screenshots of mind-map features.
 * Run from the repo root inside the devcontainer:
 *   node tools/screenshots.mjs
 */
import { chromium } from 'playwright';
import { spawn } from 'child_process';
import { existsSync, mkdirSync } from 'fs';
import { join, dirname } from 'path';
import { fileURLToPath } from 'url';
import { setTimeout as sleep } from 'timers/promises';

const __dirname = dirname(fileURLToPath(import.meta.url));
const ROOT = join(__dirname, '..');
const OUT = join(ROOT, 'docs', 'screenshots');
const PORT = process.env.SCREENSHOT_PORT || '14242';
const BASE = `http://127.0.0.1:${PORT}`;

mkdirSync(OUT, { recursive: true });

// Build if needed
const binPath = '/tmp/mind-map-bin';
if (!existsSync(binPath)) {
  console.log('Build the server first: go build -o /tmp/mind-map-bin ./cmd/mind-map/');
  process.exit(1);
}

// Start server
console.log(`Starting server on port ${PORT}...`);
const server = spawn(binPath, ['serve', '--addr', `127.0.0.1:${PORT}`], {
  cwd: ROOT,
  stdio: ['ignore', 'pipe', 'pipe'],
});

// Wait for ready
let ready = false;
for (let i = 0; i < 40; i++) {
  await sleep(500);
  try {
    const resp = await fetch(`${BASE}/api/pages`);
    if (resp.ok) { ready = true; break; }
  } catch {}
}
if (!ready) {
  console.error('Server failed to start');
  server.kill();
  process.exit(1);
}
console.log('Server ready.\n');

const browser = await chromium.launch({
  args: ['--no-sandbox', '--disable-gpu'],
});

async function capture(name, opts, fn) {
  const { dark = false, viewport = { width: 1280, height: 800 } } = opts;
  const context = await browser.newContext({
    viewport,
    deviceScaleFactor: 2,
    colorScheme: dark ? 'dark' : 'light',
  });
  const page = await context.newPage();
  try {
    await fn(page);
    await page.screenshot({ path: join(OUT, `${name}.png`), fullPage: false });
    console.log(`  ✓ ${name}.png`);
  } catch (e) {
    console.error(`  ✗ ${name}: ${e.message}`);
  }
  await context.close();
}

console.log('Capturing screenshots...\n');

// --- 1. Page view (light) ---
await capture('page-view', {}, async (page) => {
  await page.goto(`${BASE}/#/introduction`);
  await page.waitForSelector('.page-header', { timeout: 5000 });
  await sleep(600);
});

// --- 2. Page view (dark) ---
await capture('page-view-dark', { dark: true }, async (page) => {
  // Set localStorage before the app loads so it initializes in dark mode
  await page.addInitScript(() => {
    localStorage.setItem('mm-theme', 'dark');
  });
  await page.goto(`${BASE}/#/introduction`);
  await page.waitForSelector('.page-header', { timeout: 5000 });
  await sleep(300);
  // Verify dark class is present, click toggle if not
  const isDark = await page.evaluate(() =>
    document.documentElement.classList.contains('dark')
  );
  if (!isDark) {
    await page.locator('.theme-toggle').click();
    await sleep(400);
  }
  await sleep(300);
});

// --- 3. Search (with graph filtered) ---
await capture('search', {}, async (page) => {
  // Use "architecture" as a term that hits multiple pages and shows
  // a nice subgraph. Navigate to graph view first, then apply filter.
  await page.goto(`${BASE}/#/`);
  await page.waitForSelector('.sidebar', { timeout: 5000 });
  await sleep(300);
  // Go to graph view
  const graphLink = page.locator('.sidebar-header-text, [title="Show graph view"]');
  if (await graphLink.count() > 0) await graphLink.first().click();
  await sleep(1500);
  // Type search term and press Enter to activate filtering
  const searchInput = page.locator('input[type="search"], input[placeholder*="earch"], .search-input');
  if (await searchInput.count() > 0) {
    await searchInput.first().click();
    await searchInput.first().fill('architecture');
    await searchInput.first().press('Enter');
    await sleep(1500);
  }
  // Click "fit all" to center the filtered nodes nicely
  const fitBtn = page.locator('.graph-fit, button:has-text("fit all")');
  if (await fitBtn.count() > 0) {
    await fitBtn.first().click();
    await sleep(600);
  }
  // Zoom in a bit on the filtered subgraph
  await page.locator('.graph-canvas').hover();
  for (let i = 0; i < 2; i++) {
    await page.mouse.wheel(0, -120);
    await sleep(200);
  }
  await sleep(400);
});

// --- 4. Edit mode ---
await capture('edit-mode', {}, async (page) => {
  await page.goto(`${BASE}/#/introduction`);
  await page.waitForSelector('.page-header', { timeout: 5000 });
  await sleep(300);
  await page.locator('.edit-icon-btn').first().click();
  await sleep(500);
});

// --- 5. Export panel ---
await capture('export-panel', {}, async (page) => {
  await page.goto(`${BASE}/#/introduction`);
  await page.waitForSelector('.page-header', { timeout: 5000 });
  await sleep(300);
  // Click the export/share button (second edit-icon-btn)
  const btns = page.locator('.edit-icon-btn');
  if (await btns.count() >= 2) {
    await btns.nth(1).click();
  } else {
    await page.locator('button[title="Export subtree"]').click();
  }
  await sleep(600);
});

// --- 6. Graph view (zoomed in) ---
await capture('graph-view', {}, async (page) => {
  await page.goto(`${BASE}/#/`);
  await page.waitForSelector('.sidebar', { timeout: 5000 });
  await sleep(300);
  // Go to graph view
  const graphLink = page.locator('.sidebar-header-text, [title="Show graph view"]');
  if (await graphLink.count() > 0) {
    await graphLink.first().click();
  }
  await sleep(2500); // Graph layout needs time to settle
  // Zoom in by calling the graph API via the saved view
  await page.evaluate(() => {
    localStorage.setItem('mm-graph-view', JSON.stringify({ k: 2.2, x: 0, y: 0 }));
  });
  // Click "fit all" then zoom in a bit more — fit centers nicely, then we boost
  const fitBtn = page.locator('.graph-fit, button:has-text("fit all")');
  if (await fitBtn.count() > 0) {
    await fitBtn.first().click();
    await sleep(600);
  }
  // Use mouse wheel to zoom in further on the center
  await page.locator('.graph-canvas').hover();
  for (let i = 0; i < 3; i++) {
    await page.mouse.wheel(0, -120);
    await sleep(200);
  }
  await sleep(400);
});

// --- 7. Backlinks ---
await capture('backlinks', {}, async (page) => {
  // Use a page that likely has backlinks
  await page.goto(`${BASE}/#/architecture/wiki-engine`);
  await page.waitForSelector('.page-header', { timeout: 5000 });
  await sleep(500);
  // Scroll to bottom to show backlinks
  await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight));
  await sleep(400);
});

// --- 8. Settings ---
await capture('settings', {}, async (page) => {
  await page.goto(`${BASE}/#/introduction`);
  await page.waitForSelector('.sidebar', { timeout: 5000 });
  await sleep(300);
  // Open settings (gear icon in sidebar)
  const gear = page.locator('.sidebar-status button, button[title*="etting"]');
  if (await gear.count() > 0) {
    await gear.first().click();
    await sleep(500);
  }
});

await browser.close();
server.kill();
console.log(`\nDone! Screenshots saved to ${OUT}`);
