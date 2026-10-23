import assert from "node:assert/strict"
import test from "node:test"
import { copyText } from "../src/lib/clipboard.ts"

function replace(t: { after: (fn: () => void) => void }, name: string, value: unknown) {
  const previous = Object.getOwnPropertyDescriptor(globalThis, name)
  Object.defineProperty(globalThis, name, { configurable: true, value })
  t.after(() => {
    if (previous) Object.defineProperty(globalThis, name, previous)
    else Reflect.deleteProperty(globalThis, name)
  })
}

test("HTTP copy preserves the draft and focus, removes temporary fields, and reports denial", async (t) => {
  let focused: ElementMock | null = null
  const values: string[] = []
  const children: ElementMock[] = []
  let permitted = true
  class ElementMock {
    value = ""
    style = { cssText: "" }
    selectionStart = 2
    selectionEnd = 4
    selectionDirection = "backward"
    readOnly = false
    tabIndex = 0
    closest() { return null }
    focus() { focused = this }
    select() { this.selectionStart = 0; this.selectionEnd = this.value.length }
    setSelectionRange(start: number, end: number, direction: string) { this.selectionStart = start; this.selectionEnd = end; this.selectionDirection = direction }
    remove() { children.splice(children.indexOf(this), 1) }
  }
  const original = new ElementMock(); original.value = "existing task draft"; focused = original
  replace(t, "isSecureContext", false)
  replace(t, "HTMLElement", ElementMock)
  replace(t, "HTMLInputElement", ElementMock)
  replace(t, "HTMLTextAreaElement", ElementMock)
  replace(t, "document", {
    get activeElement() { return focused },
    getSelection: () => null,
    createElement: () => new ElementMock(),
    body: { appendChild: (el: ElementMock) => children.push(el) },
    execCommand: (command: string) => { assert.equal(command, "copy"); values.push(focused!.value); return permitted },
  })
  await copyText("复制中文和\n第二行")
  assert.deepEqual(values, ["复制中文和\n第二行"])
  assert.equal(focused, original)
  assert.deepEqual([original.selectionStart, original.selectionEnd, original.selectionDirection], [2, 4, "backward"])
  assert.equal(original.value, "existing task draft")
  assert.equal(children.length, 0)
  permitted = false
  await assert.rejects(copyText("not copied"), /copy failed/)
  assert.equal(children.length, 0)
  assert.equal(focused, original)
})

test("HTTPS uses native clipboard without creating temporary input", async (t) => {
  const copied: string[] = []
  replace(t, "isSecureContext", true)
  replace(t, "navigator", { clipboard: { writeText: async (text: string) => { copied.push(text) } } })
  await copyText("HTTPS text")
  assert.deepEqual(copied, ["HTTPS text"])
})
