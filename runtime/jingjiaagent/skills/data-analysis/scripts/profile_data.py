"""Deterministic data profile with provenance and formula-injection protection."""
import argparse
import hashlib
import json
from pathlib import Path
import pandas as pd
import openpyxl
from openpyxl.utils.dataframe import dataframe_to_rows


def safe_excel(value):
    if isinstance(value, str) and value.lstrip().startswith(("=", "+", "-", "@")):
        return "'" + value
    if pd.isna(value): return None
    return value.item() if hasattr(value, "item") else value


def profile(source, output, encoding="utf-8-sig"):
    if source.suffix.lower() == ".csv":
        sheets = {"CSV": pd.read_csv(source, encoding=encoding)}
    elif source.suffix.lower() == ".xlsx":
        sheets = pd.read_excel(source, sheet_name=None, engine="openpyxl")
    else: raise ValueError("only .csv and .xlsx are supported")
    output.mkdir(parents=True, exist_ok=True)
    summary = {"source": source.name, "source_sha256": hashlib.sha256(source.read_bytes()).hexdigest(), "sheets": []}
    book = openpyxl.Workbook(); book.remove(book.active)
    for index, (name, data) in enumerate(sheets.items()):
        if len(data) > 100_000 or data.size > 2_000_000: raise ValueError("spreadsheet exceeds analysis limits")
        sheet_summary = {"name": name, "rows": len(data), "columns": [str(value) for value in data.columns],
                         "duplicate_rows": int(data.duplicated().sum()), "missing": {str(key): int(value) for key, value in data.isna().sum().items()},
                         "numeric": {}}
        for column in data.select_dtypes(include="number").columns:
            series = data[column].dropna()
            sheet_summary["numeric"][str(column)] = {"count": int(series.count()), "sum": float(series.sum()),
                "mean": float(series.mean()) if len(series) else None, "min": float(series.min()) if len(series) else None,
                "max": float(series.max()) if len(series) else None}
        summary["sheets"].append(sheet_summary)
        worksheet = book.create_sheet("data_" + str(index + 1))
        worksheet.append(["source_file", "source_sheet", "source_row", *[str(column) for column in data.columns]])
        for row_number, row in enumerate(dataframe_to_rows(data, index=False, header=False), 2):
            worksheet.append([source.name, name, row_number, *[safe_excel(value) for value in row]])
        worksheet.freeze_panes = "D2"; worksheet.auto_filter.ref = worksheet.dimensions
        for column in worksheet.columns:
            worksheet.column_dimensions[column[0].column_letter].width = min(40, max(14, max(len(str(cell.value or "")) for cell in column[:200]) + 2))
    book.save(output / "analysis.xlsx")
    (output / "profile.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2, allow_nan=False), encoding="utf-8")
    return summary


if __name__ == "__main__":
    parser = argparse.ArgumentParser(); parser.add_argument("input", type=Path)
    parser.add_argument("--out", type=Path, default=Path(".jingjiaagent/output/analysis")); parser.add_argument("--encoding", default="utf-8-sig")
    args = parser.parse_args(); profile(args.input, args.out, args.encoding)
