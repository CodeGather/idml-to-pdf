from __future__ import annotations

import argparse
import os
import zipfile
from typing import Iterable
from xml.etree import ElementTree

CATALOG_ID = 1
PAGES_ID = 2
FONT_ID = 3
DEFAULT_LINES_PER_PAGE = 45
FONT_SIZE = 12
LEFT_MARGIN = 72
TOP_MARGIN = 770
LINE_SPACING = -16


def _normalize_line(text: str) -> str:
    return " ".join(text.split())


def extract_idml_text(idml_path: str) -> list[str]:
    lines: list[str] = []
    with zipfile.ZipFile(idml_path, "r") as archive:
        story_files = sorted(
            name for name in archive.namelist() if name.startswith("Stories/") and name.endswith(".xml")
        )
        for story_file in story_files:
            with archive.open(story_file) as story:
                root = ElementTree.parse(story).getroot()
                for element in root.iter():
                    if element.tag.endswith("Content") and element.text:
                        normalized = _normalize_line(element.text)
                        if normalized:
                            lines.append(normalized)
    return lines


def _escape_pdf_text(text: str) -> str:
    sanitized = text.encode("latin-1", "replace").decode("latin-1")
    return sanitized.replace("\\", "\\\\").replace("(", "\\(").replace(")", "\\)")


def _page_chunks(lines: Iterable[str], lines_per_page: int = DEFAULT_LINES_PER_PAGE) -> list[list[str]]:
    source = list(lines) or [""]
    return [source[index : index + lines_per_page] for index in range(0, len(source), lines_per_page)]


def build_pdf(lines: Iterable[str]) -> bytes:
    pages = _page_chunks(lines)
    object_map: dict[int, str] = {
        CATALOG_ID: f"<< /Type /Catalog /Pages {PAGES_ID} 0 R >>",
        FONT_ID: "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    }
    page_refs: list[str] = []

    next_id = 4
    for page_lines in pages:
        page_id = next_id
        content_id = next_id + 1
        next_id += 2

        commands = ["BT", f"/F1 {FONT_SIZE} Tf", f"{LEFT_MARGIN} {TOP_MARGIN} Td"]
        for index, line in enumerate(page_lines):
            escaped = _escape_pdf_text(line)
            if index > 0:
                commands.append(f"0 {LINE_SPACING} Td")
            commands.append(f"({escaped}) Tj")
        commands.append("ET")
        stream = "\n".join(commands).encode("latin-1")

        object_map[content_id] = f"<< /Length {len(stream)} >>\nstream\n{stream.decode('latin-1')}\nendstream"
        object_map[page_id] = (
            f"<< /Type /Page /Parent {PAGES_ID} 0 R /MediaBox [0 0 612 792] "
            f"/Resources << /Font << /F1 {FONT_ID} 0 R >> >> /Contents {content_id} 0 R >>"
        )
        page_refs.append(f"{page_id} 0 R")

    object_map[PAGES_ID] = f"<< /Type /Pages /Count {len(page_refs)} /Kids [{' '.join(page_refs)}] >>"

    object_ids = sorted(object_map)
    chunks = [b"%PDF-1.4\n"]
    offsets = {0: 0}

    for object_id in object_ids:
        offsets[object_id] = sum(len(chunk) for chunk in chunks)
        chunks.append(f"{object_id} 0 obj\n{object_map[object_id]}\nendobj\n".encode("latin-1"))

    xref_offset = sum(len(chunk) for chunk in chunks)
    max_id = max(object_ids)
    xref = [f"xref\n0 {max_id + 1}\n", "0000000000 65535 f \n"]
    for object_id in range(1, max_id + 1):
        xref.append(f"{offsets[object_id]:010d} 00000 n \n")
    chunks.append("".join(xref).encode("latin-1"))
    chunks.append(
        f"trailer\n<< /Size {max_id + 1} /Root {CATALOG_ID} 0 R >>\nstartxref\n{xref_offset}\n%%EOF\n".encode("latin-1")
    )
    return b"".join(chunks)


def convert_idml_to_pdf(idml_path: str, pdf_path: str) -> None:
    lines = extract_idml_text(idml_path)
    pdf_bytes = build_pdf(lines)
    with open(pdf_path, "wb") as pdf_file:
        pdf_file.write(pdf_bytes)


def main() -> None:
    parser = argparse.ArgumentParser(description="Convert text content from an IDML file to a simple PDF.")
    parser.add_argument("input_idml", help="Path to the input IDML file")
    parser.add_argument("output_pdf", help="Path to the output PDF file")
    args = parser.parse_args()

    if not os.path.exists(args.input_idml):
        raise FileNotFoundError(f"Input IDML file not found: {args.input_idml}")

    convert_idml_to_pdf(args.input_idml, args.output_pdf)


if __name__ == "__main__":
    main()
