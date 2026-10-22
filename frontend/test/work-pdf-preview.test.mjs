import assert from "node:assert/strict";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { workPdfOptions, workPdfPageNumber } from "../src/lib/work-pdf.ts";
import { pdfjsOfflineAssets } from "../scripts/pdfjs-assets.ts";

const requirePdf = createRequire(import.meta.resolve("pdfjs-dist/package.json"));
const { createCanvas, DOMMatrix, ImageData, Path2D } = requirePdf("@napi-rs/canvas");
Object.assign(globalThis, { DOMMatrix, ImageData, Path2D });
const pdfjs = await import("pdfjs-dist/legacy/build/pdf.mjs");
const packageRoot = path.dirname(fileURLToPath(import.meta.resolve("pdfjs-dist/package.json")));

function twoPagePdf() {
  const stream = (text) => `BT /F1 18 Tf 20 100 Td (${text}) Tj ET`;
  const objects = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    "<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
    "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 160] /Resources << /Font << /F1 7 0 R >> >> /Contents 5 0 R >>",
    "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 160] /Resources << /Font << /F1 7 0 R >> >> /Contents 6 0 R >>",
    `<< /Length ${stream("First page").length} >>\nstream\n${stream("First page")}\nendstream`,
    `<< /Length ${stream("Second page").length} >>\nstream\n${stream("Second page")}\nendstream`,
    "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
  ];
  let data = "%PDF-1.4\n";
  const offsets = [0];
  for (let i = 0; i < objects.length; i++) {
    offsets.push(data.length);
    data += `${i + 1} 0 obj\n${objects[i]}\nendobj\n`;
  }
  const start = data.length;
  data += `xref\n0 ${objects.length + 1}\n0000000000 65535 f \n`;
  for (const offset of offsets.slice(1)) data += `${String(offset).padStart(10, "0")} 00000 n \n`;
  data += `trailer\n<< /Size ${objects.length + 1} /Root 1 0 R >>\nstartxref\n${start}\n%%EOF\n`;
  return new Uint8Array(Buffer.from(data));
}

test("本地PDF渲染器实际解析两页、分别绘制文本且不发公网请求", async (t) => {
  t.mock.method(globalThis, "fetch", () => { throw new Error("PDF preview must stay offline"); });
  const options = workPdfOptions(twoPagePdf(), "http://127.0.0.1:47424/");
  const loading = pdfjs.getDocument({
    ...options,
    standardFontDataUrl: packageRoot.replace(/\\/g, "/") + "/standard_fonts/",
    cMapUrl: packageRoot.replace(/\\/g, "/") + "/cmaps/",
    wasmUrl: packageRoot.replace(/\\/g, "/") + "/wasm/",
    iccUrl: packageRoot.replace(/\\/g, "/") + "/iccs/",
  });
  try {
    const document = await loading.promise;
    assert.equal(document.numPages, 2);
    for (const [number, text] of [[1, "First page"], [2, "Second page"]]) {
      const page = await document.getPage(number);
      const content = await page.getTextContent();
      assert.equal(content.items.map((item) => item.str || "").join(""), text);
      const viewport = page.getViewport({ scale: 1 });
      const canvas = createCanvas(viewport.width, viewport.height);
      const context = canvas.getContext("2d");
      await page.render({ canvas, canvasContext: context, viewport, background: "white" }).promise;
      const pixels = context.getImageData(0, 0, canvas.width, canvas.height).data;
      assert.ok(pixels.some((value, index) => index % 4 === 0 && value < 100), `page ${number} has no rendered text`);
    }
  } finally {
    await loading.destroy();
  }
});

test("PDF支持文件随离线构建输出，全部同源且不打包脚本执行器", () => {
  const options = workPdfOptions(new Uint8Array([1]), "http://127.0.0.1:47424/");
  for (const name of ["cMapUrl", "standardFontDataUrl", "wasmUrl", "iccUrl"]) {
    assert.equal(new URL(options[name]).origin, "http://127.0.0.1:47424");
  }
  assert.equal(options.isEvalSupported, false);
  assert.equal(options.enableXfa, false);
  assert.equal("url" in options, false);
  const assets = [];
  pdfjsOfflineAssets().generateBundle.call({ emitFile(asset) { assets.push(asset); } });
  for (const filename of ["pdfjs/cmaps/UniGB-UCS2-H.bcmap", "pdfjs/standard_fonts/LiberationSans-Regular.ttf", "pdfjs/wasm/openjpeg.wasm", "pdfjs/LICENSE"]) {
    assert.ok(assets.some((asset) => asset.fileName === filename && asset.source.length > 0), filename);
  }
  assert.ok(assets.every((asset) => !asset.fileName.includes("quickjs-eval")));
  assert.equal(workPdfPageNumber(0, 2), 1);
  assert.equal(workPdfPageNumber(3, 2), 2);
});
