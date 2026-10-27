import assert from "node:assert/strict"
import { readFileSync, readdirSync } from "node:fs"
import test from "node:test"

test("所有 AlertDialog 使用默认宽度并把提示文字放在标题之外", () => {
  const root = new URL("../src/", import.meta.url)
  let count = 0
  for (const path of readdirSync(root, { recursive: true })) {
    if (!path.endsWith(".tsx")) continue
    const content = readFileSync(new URL(path, root), "utf8")
    count += (content.match(/<AlertDialog([ >]|$)/gm) ?? []).length
    assert.doesNotMatch(content, /<AlertDialogContent\b[^>]*\bsize="sm"/, path)
    assert.doesNotMatch(
      content,
      /<AlertDialogHeader\b[^>]*>(?:(?!<\/AlertDialogHeader>)[\s\S])*?<AlertDialogDescription\b/,
      path,
    )
  }
  assert.ok(count > 0)
  const primitive = readFileSync(
    new URL("../src/components/ui/alert-dialog.tsx", import.meta.url),
    "utf8",
  )
  assert.match(primitive, /max-w-xs[^"\n]*sm:max-w-lg/)
  assert.doesNotMatch(primitive, /data-\[size=sm\]|size\?: "default" \| "sm"/)
})
