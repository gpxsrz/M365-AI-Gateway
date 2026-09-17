"""Deterministic, non-mail fixtures for native attachment qualification tests."""

from __future__ import annotations

import io
import struct
import zipfile
import zlib


XLSX_FILENAME = "m365-native-sentinel.xlsx"
XLSX_SENTINEL = "M365_NATIVE_XLSX_SENTINEL"
PNG_FILENAME = "m365-native-sentinel.png"
PNG_SENTINEL = "M365_NATIVE_PNG_SENTINEL"
UNKNOWN_FILENAME = "m365-native-sentinel.opaque"
UNKNOWN_BYTES = b"M365_NATIVE_UNKNOWN_EXTENSION_SENTINEL\nrow=42\n"
LARGE_STAGED_SIZE = (16 << 20) + 1


def xlsx_fixture() -> bytes:
    """Return a minimal XLSX ZIP with a real worksheet and table sentinel."""

    parts = {
        "[Content_Types].xml": """<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
  <Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
  <Override PartName="/xl/tables/table1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.table+xml"/>
</Types>
""",
        "_rels/.rels": """<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>
""",
        "xl/workbook.xml": """<?xml version="1.0" encoding="UTF-8"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets><sheet name="NativeSentinel" sheetId="1" r:id="rId1"/></sheets>
</workbook>
""",
        "xl/_rels/workbook.xml.rels": """<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
</Relationships>
""",
        "xl/worksheets/sheet1.xml": f"""<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheetData>
    <row r="1"><c r="A1" t="inlineStr"><is><t>case</t></is></c><c r="B1" t="inlineStr"><is><t>value</t></is></c></row>
    <row r="2"><c r="A2" t="inlineStr"><is><t>{XLSX_SENTINEL}</t></is></c><c r="B2"><v>42</v></c></row>
    <row r="3"><c r="A3" t="inlineStr"><is><t>status</t></is></c><c r="B3" t="inlineStr"><is><t>PASS</t></is></c></row>
  </sheetData>
  <tableParts count="1"><tablePart r:id="rId1"/></tableParts>
</worksheet>
""",
        "xl/worksheets/_rels/sheet1.xml.rels": """<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/table" Target="../tables/table1.xml"/>
</Relationships>
""",
        "xl/tables/table1.xml": """<?xml version="1.0" encoding="UTF-8"?>
<table xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" id="1" name="NativeSentinelTable" displayName="NativeSentinelTable" ref="A1:B3">
  <autoFilter ref="A1:B3"/>
  <tableColumns count="2"><tableColumn id="1" name="case"/><tableColumn id="2" name="value"/></tableColumns>
  <tableStyleInfo name="TableStyleMedium2" showFirstColumn="0" showLastColumn="0" showRowStripes="1" showColumnStripes="0"/>
</table>
""",
    }
    output = io.BytesIO()
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED) as archive:
        for name in sorted(parts):
            info = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = 0o600 << 16
            archive.writestr(info, parts[name].encode("utf-8"))
    return output.getvalue()


_GLYPHS = {
    "3": ("11110", "00001", "00110", "00001", "00001", "10001", "01110"),
    "5": ("11111", "10000", "11110", "00001", "00001", "10001", "01110"),
    "E": ("11111", "10000", "10000", "11110", "10000", "10000", "11111"),
    "I": ("11111", "00100", "00100", "00100", "00100", "00100", "11111"),
    "L": ("10000", "10000", "10000", "10000", "10000", "10000", "11111"),
    "M": ("10001", "11011", "10101", "10101", "10001", "10001", "10001"),
    "N": ("10001", "11001", "10101", "10011", "10001", "10001", "10001"),
    "S": ("01111", "10000", "10000", "01110", "00001", "00001", "11110"),
    "T": ("11111", "00100", "00100", "00100", "00100", "00100", "00100"),
}


def _png_chunk(kind: bytes, data: bytes) -> bytes:
    return (
        struct.pack(">I", len(data))
        + kind
        + data
        + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)
    )


def png_fixture() -> bytes:
    """Return a valid RGB PNG whose pixels visibly spell ``SENTINEL``."""

    text = "SENTINEL"
    scale = 4
    margin = 4
    width = margin * 2 + (len(text) * 5 + len(text) - 1) * scale
    height = margin * 2 + 7 * scale
    white = b"\xff\xff\xff"
    black = b"\x10\x10\x10"
    rows = []
    for y in range(height):
        row = bytearray(b"\x00" + white * width)
        glyph_y = (y - margin) // scale
        if 0 <= glyph_y < 7:
            for index, character in enumerate(text):
                glyph = _GLYPHS[character][glyph_y]
                start = margin + (index * 6) * scale
                for glyph_x, bit in enumerate(glyph):
                    if bit == "1":
                        for pixel_x in range(scale):
                            offset = 1 + ((start + glyph_x * scale + pixel_x) * 3)
                            row[offset : offset + 3] = black
        rows.append(bytes(row))
    raw = b"".join(rows)
    return b"\x89PNG\r\n\x1a\n" + _png_chunk(
        b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    ) + _png_chunk(b"tEXt", b"Comment\0" + PNG_SENTINEL.encode("ascii")) + _png_chunk(
        b"IDAT", zlib.compress(raw, 9)
    ) + _png_chunk(b"IEND", b"")


def unknown_extension_fixture() -> tuple[str, bytes]:
    return UNKNOWN_FILENAME, UNKNOWN_BYTES
