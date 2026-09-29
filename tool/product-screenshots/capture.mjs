// Renders the product screenshots used by the README and vibe-agi.github.io
// from a Flutter Web build of the Preview dataset. See README.md.
import { createServer } from "node:http";
import { readFile, mkdir, writeFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import { chromium } from "playwright";

const [webRoot, outDir] = process.argv.slice(2);
if (!webRoot || !outDir) {
  console.error("usage: node capture.mjs <preview web build> <output directory>");
  process.exit(64);
}

// The Preview dataset is anchored at this instant; freezing the browser clock
// there keeps relative ages ("3m") and times stable in every capture.
const previewNow = new Date("2026-08-10T09:44:00Z");
const viewport = { width: 1440, height: 804 };
const scale = 5 / 3; // 2400 device pixels wide
const sizes = [2400, 1280];

const labels = {
  en: {
    traffic: "Traffic", insights: "Insights", configuration: "Configuration",
    connections: "Connections", policies: "Traffic policies",
    accounts: "Upstream accounts", scripts: "Script library",
  },
  zh: {
    traffic: "\u6d41\u91cf", insights: "\u6d1e\u5bdf", configuration: "\u914d\u7f6e",
    connections: "\u8fde\u63a5", policies: "\u6d41\u91cf\u7b56\u7565",
    accounts: "\u4e0a\u6e38\u8d26\u53f7", scripts: "\u811a\u672c\u5e93",
  },
};

// Each scene starts from a fresh page so one scene cannot leave a dialog or
// selection behind for the next.
const scenes = {
  conversation: async () => {},
  approval: async (ui, l) => {
    await ui.tab(l.connections);
  },
  routes: async (ui, l) => {
    await ui.button(l.configuration);
    await ui.button("Work");
  },
  usage: async (ui, l) => {
    await ui.button(l.insights);
  },
  accounts: async (ui, l) => {
    await ui.button(l.configuration);
    await ui.tab(l.accounts);
  },
  scripts: async (ui, l) => {
    await ui.button(l.configuration);
    await ui.tab(l.scripts);
  },
};

const types = {
  ".html": "text/html", ".js": "text/javascript", ".mjs": "text/javascript",
  ".json": "application/json", ".wasm": "application/wasm", ".css": "text/css",
  ".png": "image/png", ".svg": "image/svg+xml", ".otf": "font/otf",
  ".ttf": "font/ttf", ".bin": "application/octet-stream",
};
const server = createServer(async (request, response) => {
  const path = normalize(decodeURIComponent(new URL(request.url, "http://x").pathname));
  const file = join(webRoot, path.endsWith("/") ? `${path}index.html` : path);
  try {
    const body = await readFile(file);
    response.writeHead(200, { "content-type": types[extname(file)] ?? "application/octet-stream" });
    response.end(body);
  } catch {
    response.writeHead(404).end();
  }
});
// The footer shows the Runtime address; serve on the documented default.
await new Promise((resolve) => server.listen(9666, "127.0.0.1", resolve));
const origin = `http://127.0.0.1:${server.address().port}/`;

const browser = await chromium.launch();
await mkdir(outDir, { recursive: true });
const encoder = await browser.newPage();

// Chromium wraps its WebP in an extended container only to embed an sRGB ICC
// profile. Keep just the lossy VP8 bitstream in a simple container.
function simpleWebP(webp) {
  for (let offset = 12; offset + 8 <= webp.length;) {
    const size = webp.readUInt32LE(offset + 4);
    if (webp.toString("ascii", offset, offset + 4) === "VP8 ") {
      const chunk = webp.subarray(offset, offset + 8 + size + (size & 1));
      const header = Buffer.alloc(12);
      header.write("RIFF", 0, "ascii");
      header.writeUInt32LE(4 + chunk.length, 4);
      header.write("WEBP", 8, "ascii");
      return Buffer.concat([header, chunk]);
    }
    offset += 8 + size + (size & 1);
  }
  throw new Error("encoded WebP has no lossy VP8 bitstream");
}

async function encode(png, width) {
  return simpleWebP(Buffer.from(
    await encoder.evaluate(
      async ({ data, width }) => {
        const image = new Image();
        image.src = `data:image/png;base64,${data}`;
        await image.decode();
        const canvas = document.createElement("canvas");
        canvas.width = width;
        canvas.height = Math.round((image.height * width) / image.width);
        const context = canvas.getContext("2d", { alpha: false });
        context.imageSmoothingQuality = "high";
        context.drawImage(image, 0, 0, canvas.width, canvas.height);
        return canvas.toDataURL("image/webp", 0.82).split(",")[1];
      },
      { data: png.toString("base64"), width },
    ),
    "base64",
  ));
}

function driver(page) {
  const settle = (ms) => page.clock.runFor(ms);
  async function press(role, name) {
    // Flutter builds semantics asynchronously; retry briefly in real time.
    for (let attempt = 0; attempt < 20; attempt++) {
      for (const handle of await page.$$(`flt-semantics[role=${role}]`)) {
        const label = await handle.evaluate((node) =>
          (node.getAttribute("aria-label") || node.textContent || "").trim());
        if (label === name || label.startsWith(`${name} `) || label.startsWith(`${name},`) ||
            label.startsWith(`${name}\n`)) {
          await handle.click({ force: true });
          await settle(1800);
          return;
        }
      }
      await settle(250);
      await page.waitForTimeout(250);
    }
    throw new Error(`no ${role} named ${JSON.stringify(name)}`);
  }
  return {
    settle,
    button: (name) => press("button", name),
    tab: (name) => press("tab", name),
  };
}

let failed = false;
// The approval card is short; a shorter frame avoids an empty screen.
const heights = { approval: 420 };

for (const locale of ["en", "zh"]) {
  for (const [scene, steps] of Object.entries(scenes)) {
    const sceneViewport = { ...viewport, height: heights[scene] ?? viewport.height };
    const context = await browser.newContext({
      viewport: sceneViewport, deviceScaleFactor: scale,
      locale: locale === "zh" ? "zh-CN" : "en-US", timezoneId: "Asia/Shanghai",
    });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(String(error)));
    await page.clock.install({ time: previewNow });
    await page.clock.pauseAt(previewNow);
    await page.goto(origin);
    await page.clock.runFor(4000);
    await page.waitForSelector("flt-semantics-placeholder", { state: "attached" });
    await page.evaluate(() => document.querySelector("flt-semantics-placeholder")?.click());
    const ui = driver(page);
    await ui.settle(1500);
    await steps(ui, labels[locale]);
    await page.mouse.move(sceneViewport.width - 1, sceneViewport.height - 1);
    await ui.settle(1500);
    const png = await page.screenshot();
    for (const width of sizes) {
      await writeFile(join(outDir, `${scene}-${locale}-${width}.webp`), await encode(png, width));
    }
    if (errors.length > 0) {
      failed = true;
      console.error(`${scene}/${locale}: ${errors.join("; ")}`);
    }
    console.log(`${scene}-${locale}`);
    await context.close();
  }
}
await browser.close();
server.close();
process.exit(failed ? 1 : 0);
