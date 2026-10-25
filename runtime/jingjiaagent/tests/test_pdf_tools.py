"""Real-PDF regressions for the offline helper; no additional fixture libraries."""
import contextlib
import csv
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from openpyxl import load_workbook
from pypdf import PdfReader, PdfWriter
from pypdf.generic import ArrayObject, BooleanObject, DecodedStreamObject, DictionaryObject, NameObject, NumberObject, TextStringObject


SKILLS_ROOT = Path(os.environ.get("JINGJIAAGENT_SKILLS_ROOT", Path(__file__).resolve().parents[1] / "skills"))
SCRIPT = SKILLS_ROOT / "report-writing/scripts/pdf_tools.py"
spec = importlib.util.spec_from_file_location("pdf_tools", SCRIPT)
pdf_tools = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pdf_tools)


def make_pdf(path, contents):
    """Write ordinary PDF objects, text and ruled tables, with a valid xref."""
    objects = [b"<< /Type /Catalog /Pages 2 0 R >>", b"",
               b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"]
    pages = []
    for content in contents:
        page_number = len(objects) + 1
        pages.append(page_number)
        objects.append(f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "
                       f"/Resources << /Font << /F1 3 0 R >> >> /Contents {page_number + 1} 0 R >>".encode())
        data = content.encode("ascii")
        objects.append(b"<< /Length " + str(len(data)).encode() + b" >>\nstream\n" + data + b"\nendstream")
    objects[1] = f"<< /Type /Pages /Count {len(pages)} /Kids [".encode() + b" ".join(
        f"{page} 0 R".encode() for page in pages) + b"] >>"
    data, positions = bytearray(b"%PDF-1.4\n"), [0]
    for number, value in enumerate(objects, 1):
        positions.append(len(data))
        data.extend(f"{number} 0 obj\n".encode() + value + b"\nendobj\n")
    xref = len(data)
    data.extend(f"xref\n0 {len(objects) + 1}\n0000000000 65535 f \n".encode())
    for position in positions[1:]:
        data.extend(f"{position:010d} 00000 n \n".encode())
    data.extend(f"trailer\n<< /Size {len(objects) + 1} /Root 1 0 R >>\nstartxref\n{xref}\n%%EOF\n".encode())
    path.write_bytes(data)


def text(value, x=60, y=700):
    escaped = value.replace("\\", "\\\\").replace("(", "\\(").replace(")", "\\)")
    return f"BT /F1 12 Tf {x} {y} Td ({escaped}) Tj ET\n"


def make_form_pdf(source, output, xfa_only=False):
    writer = PdfWriter()
    writer.clone_document_from_reader(PdfReader(source))
    form = DictionaryObject()
    if xfa_only:
        stream = DecodedStreamObject()
        stream.set_data(
            b'<xfa:datasets xmlns:xfa="http://www.xfa.org/schema/xfa-data/1.0/">'
            b'<xfa:data><employee>Alice</employee></xfa:data></xfa:datasets>'
        )
        form[NameObject("/Fields")] = ArrayObject()
        form[NameObject("/XFA")] = ArrayObject([TextStringObject("datasets"), writer._add_object(stream)])
    else:
        field = DictionaryObject({
            NameObject("/Type"): NameObject("/Annot"),
            NameObject("/Subtype"): NameObject("/Widget"),
            NameObject("/FT"): NameObject("/Tx"),
            NameObject("/T"): TextStringObject("employee"),
            NameObject("/V"): TextStringObject("Alice"),
            NameObject("/Rect"): ArrayObject([NumberObject(value) for value in (30, 40, 230, 70)]),
            NameObject("/F"): NumberObject(4),
            NameObject("/DA"): TextStringObject("/Helv 12 Tf 0 g"),
        })
        font = DictionaryObject({
            NameObject("/Type"): NameObject("/Font"),
            NameObject("/Subtype"): NameObject("/Type1"),
            NameObject("/BaseFont"): NameObject("/Helvetica"),
        })
        ref = writer._add_object(field)
        writer.pages[0][NameObject("/Annots")] = ArrayObject([ref])
        form[NameObject("/Fields")] = writer._add_object(ArrayObject([ref]))
        form[NameObject("/NeedAppearances")] = BooleanObject(True)
        form[NameObject("/DR")] = DictionaryObject({
            NameObject("/Font"): DictionaryObject({NameObject("/Helv"): writer._add_object(font)}),
        })
    writer._root_object[NameObject("/AcroForm")] = writer._add_object(form)
    writer.write(output)


class PDFToolsTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "source.pdf"
        make_pdf(self.source, [text("First page"), text("Second page"), ""])
        self.original = hashlib.sha256(self.source.read_bytes()).digest()

    def tearDown(self):
        self.assertEqual(hashlib.sha256(self.source.read_bytes()).digest(), self.original)

    def test_text_preserves_page_provenance_and_warns_about_empty_page(self):
        output = self.root / "nested/text.json"
        result = pdf_tools.extract_text(self.source, output)
        self.assertEqual(json.loads(output.read_text(encoding="utf-8")), result)
        self.assertEqual(result["source_file"], "source.pdf")
        self.assertEqual(result["source_sha256"], self.original.hex())
        self.assertEqual([page["page"] for page in result["pages"]], [1, 2, 3])
        self.assertIn("Second page", result["pages"][1]["text"])
        self.assertIn("OCR", result["warnings"][0])
        output = self.root / "text.txt"
        pdf_tools.extract_text(self.source, output, "2", "text")
        content = output.read_text(encoding="utf-8")
        self.assertIn("第 2 页", content)
        self.assertNotIn("First page", content)

    def test_chinese_unicode_text_layer_is_preserved(self):
        source = self.root / "中文资料.pdf"
        writer = PdfWriter()
        page = writer.add_blank_page(width=612, height=792)
        cid_font = DictionaryObject({
            NameObject("/Type"): NameObject("/Font"),
            NameObject("/Subtype"): NameObject("/CIDFontType0"),
            NameObject("/BaseFont"): NameObject("/STSong-Light"),
            NameObject("/CIDSystemInfo"): DictionaryObject({
                NameObject("/Registry"): TextStringObject("Adobe"),
                NameObject("/Ordering"): TextStringObject("GB1"),
                NameObject("/Supplement"): NumberObject(4),
            }),
        })
        font = DictionaryObject({
            NameObject("/Type"): NameObject("/Font"),
            NameObject("/Subtype"): NameObject("/Type0"),
            NameObject("/BaseFont"): NameObject("/STSong-Light"),
            NameObject("/Encoding"): NameObject("/UniGB-UCS2-H"),
            NameObject("/DescendantFonts"): ArrayObject([cid_font]),
        })
        page[NameObject("/Resources")] = DictionaryObject({
            NameObject("/Font"): DictionaryObject({NameObject("/F1"): font}),
        })
        stream = DecodedStreamObject()
        stream.set_data(b"BT /F1 12 Tf 50 700 Td <4E2D65878D446599> Tj ET")
        page[NameObject("/Contents")] = stream
        writer.write(source)
        result = pdf_tools.extract_text(source, self.root / "chinese.json")
        self.assertIn("中文资料", result["pages"][0]["text"])
        self.assertEqual(result["warnings"], [])

    def test_table_exports_values_provenance_and_safe_formula_cells(self):
        source = self.root / "table.pdf"
        rules = "".join(f"50 {y} m 250 {y} l S\n" for y in (600, 625, 650, 675))
        rules += "".join(f"{x} 600 m {x} 675 l S\n" for x in (50, 150, 250))
        content = rules + text("Name", 60, 657) + text("Amount", 160, 657)
        content += text("Alpha", 60, 632) + text("12.5", 160, 632)
        content += text("=1+1", 60, 607) + text("-8", 160, 607)
        make_pdf(source, [content])
        csv_path = self.root / "table.csv"
        result = pdf_tools.extract_tables(source, csv_path)
        self.assertEqual((result["tables"], result["rows"]), (1, 3))
        with csv_path.open(encoding="utf-8-sig", newline="") as stream:
            rows = list(csv.reader(stream))
        self.assertEqual(rows[0][:4], ["source_file", "source_page", "source_table", "source_row"])
        self.assertEqual(rows[2], ["table.pdf", "1", "1", "2", "Alpha", "12.5"])
        self.assertEqual(rows[3][-2:], ["'=1+1", "'-8"])
        xlsx_path = self.root / "table.xlsx"
        pdf_tools.extract_tables(source, xlsx_path)
        with contextlib.closing(load_workbook(xlsx_path)) as book:
            self.assertEqual(book["tables"]["E4"].data_type, "s")
            self.assertEqual(book["tables"]["E4"].value, "'=1+1")
            self.assertEqual(book["tables"]["F3"].value, "12.5")
            self.assertEqual(book["source"]["B2"].value, hashlib.sha256(source.read_bytes()).hexdigest())

    def test_table_absence_has_explicit_warning(self):
        result = pdf_tools.extract_tables(self.source, self.root / "empty.csv", "3")
        self.assertEqual(result["tables"], 0)
        self.assertIn("未检测到", result["warnings"][0])

    def test_page_extract_and_merge_preserve_order(self):
        one, two = self.root / "one.pdf", self.root / "two.pdf"
        pdf_tools.extract_pages(self.source, one, "2,1")
        pdf_tools.extract_pages(self.source, two, "1")
        output = self.root / "merged.pdf"
        pdf_tools.merge_pdfs([one, two], output)
        reader = PdfReader(output)
        self.assertEqual(len(reader.pages), 3)
        self.assertEqual([page.extract_text().strip() for page in reader.pages],
                         ["Second page", "First page", "First page"])

    def test_form_page_assembly_is_rejected_without_output_or_input_changes(self):
        for xfa_only in (False, True):
            with self.subTest(xfa_only=xfa_only):
                source = self.root / ("xfa.pdf" if xfa_only else "filled-form.pdf")
                make_form_pdf(self.source, source, xfa_only)
                original = source.read_bytes()
                if not xfa_only:
                    self.assertEqual(PdfReader(source).get_fields()["employee"]["/V"], "Alice")
                operations = (
                    lambda output: pdf_tools.extract_pages(source, output, "1"),
                    lambda output: pdf_tools.merge_pdfs([source], output),
                    lambda output: pdf_tools.merge_pdfs([self.source, source], output),
                )
                for index, operation in enumerate(operations):
                    output = self.root / f"rejected-{xfa_only}-{index}.pdf"
                    with self.subTest(operation=index), self.assertRaisesRegex(pdf_tools.PdfToolError, "AcroForm/XFA"):
                        operation(output)
                    self.assertFalse(output.exists())
                    self.assertEqual(source.read_bytes(), original)

    def test_form_text_and_tables_remain_available(self):
        for xfa_only in (False, True):
            with self.subTest(xfa_only=xfa_only):
                source = self.root / f"form-{xfa_only}.pdf"
                make_form_pdf(self.source, source, xfa_only)
                original = source.read_bytes()
                result = pdf_tools.extract_text(source, self.root / f"form-{xfa_only}.json", "1")
                self.assertIn("First page", result["pages"][0]["text"])
                output = self.root / f"form-{xfa_only}.csv"
                pdf_tools.extract_tables(source, output, "1")
                self.assertTrue(output.exists())
                self.assertEqual(source.read_bytes(), original)

    def test_form_cli_rejection_explains_next_step_without_creating_output(self):
        source = self.root / "filled-form.pdf"
        make_form_pdf(self.source, source)
        output = self.root / "rejected.pdf"
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr):
            result = pdf_tools.main(["extract", str(source), "--pages", "1", "--output", str(output)])
        self.assertEqual(result, 1)
        self.assertIn("普通 PDF 副本", stderr.getvalue())
        self.assertNotIn("Traceback", stderr.getvalue())
        self.assertFalse(output.exists())

    def test_existing_files_and_inputs_cannot_be_overwritten(self):
        operations = (
            lambda: pdf_tools.extract_text(self.source, self.source),
            lambda: pdf_tools.extract_pages(self.source, self.source, "1"),
            lambda: pdf_tools.merge_pdfs([self.source], self.source),
        )
        for operation in operations:
            with self.subTest(operation=operation), self.assertRaisesRegex(pdf_tools.PdfToolError, "禁止覆盖"):
                operation()
        existing = self.root / "existing.xlsx"
        existing.write_bytes(b"original result")
        with self.assertRaisesRegex(pdf_tools.PdfToolError, "禁止覆盖"):
            pdf_tools.extract_tables(self.source, existing)
        self.assertEqual(existing.read_bytes(), b"original result")

    def test_bad_pages_are_rejected_before_output(self):
        for pages in ("0", "4", "2-1", "1-4", "1,1", "1,", "", "1.5", "-1"):
            with self.subTest(pages=pages), self.assertRaises(pdf_tools.PdfToolError):
                pdf_tools.extract_pages(self.source, self.root / "bad.pdf", pages)
        self.assertFalse((self.root / "bad.pdf").exists())

    def test_corrupt_and_encrypted_files_report_errors(self):
        broken = self.root / "broken.pdf"
        for data in (b"not a PDF", b"%PDF-1.4\ncorrupt\n%%EOF"):
            broken.write_bytes(data)
            with self.assertRaises(pdf_tools.PdfToolError):
                pdf_tools.extract_text(broken, self.root / "bad.json")
        encrypted = self.root / "encrypted.pdf"
        writer = PdfWriter()
        writer.add_blank_page(width=100, height=100)
        writer.encrypt("private")
        writer.write(encrypted)
        with self.assertRaisesRegex(pdf_tools.PdfToolError, "加密"):
            pdf_tools.extract_text(encrypted, self.root / "bad.json")
        self.assertFalse((self.root / "bad.json").exists())

    def test_limits_reject_input_page_count_output_and_merge(self):
        with patch.object(pdf_tools, "MAX_BYTES", 8):
            with self.assertRaisesRegex(pdf_tools.PdfToolError, "输入文件超过"):
                pdf_tools.read_pdf(self.source)
            with self.assertRaisesRegex(pdf_tools.PdfToolError, "输出超过"):
                pdf_tools.save_new(self.root / "huge.txt", b"123456789")
            with self.assertRaises(pdf_tools.PdfToolError):
                pdf_tools.LimitedBuffer().write(b"123456789")
        with patch.object(pdf_tools, "MAX_PAGES", 2):
            with self.assertRaisesRegex(pdf_tools.PdfToolError, "页数"):
                pdf_tools.read_pdf(self.source)
        with patch.object(pdf_tools, "MAX_PAGES", 4):
            with self.assertRaisesRegex(pdf_tools.PdfToolError, "合并输入"):
                pdf_tools.merge_pdfs([self.source, self.source], self.root / "huge.pdf")
        self.assertFalse((self.root / "huge.txt").exists())
        self.assertFalse((self.root / "huge.pdf").exists())

    def test_cli_reports_actionable_failure_without_traceback(self):
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr):
            result = pdf_tools.main(["extract", str(self.source), "--pages", "9", "--output", str(self.root / "bad.pdf")])
        self.assertEqual(result, 1)
        self.assertIn("页码", stderr.getvalue())
        self.assertNotIn("Traceback", stderr.getvalue())

    def test_cli_creates_output_and_emits_json_summary(self):
        output = self.root / "cli.json"
        stdout = io.StringIO()
        with contextlib.redirect_stdout(stdout):
            result = pdf_tools.main(["text", str(self.source), "--pages", "1", "--output", str(output)])
        self.assertEqual(result, 0)
        self.assertEqual(json.loads(stdout.getvalue())["output"], str(output))
        self.assertEqual(json.loads(output.read_text(encoding="utf-8"))["pages"][0]["page"], 1)


if __name__ == "__main__":
    unittest.main()
