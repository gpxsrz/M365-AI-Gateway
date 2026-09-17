import importlib.util
import io
import struct
import unittest
import zipfile
import zlib
from pathlib import Path


FIXTURE_PATH = Path(__file__).with_name("synthetic_fixtures.py")
SPEC = importlib.util.spec_from_file_location("m365_native_synthetic_fixtures", FIXTURE_PATH)
fixtures = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(fixtures)


class SyntheticFixtureTests(unittest.TestCase):
    def test_xlsx_contains_a_table_and_explicit_sentinel(self):
        with zipfile.ZipFile(io.BytesIO(fixtures.xlsx_fixture())) as archive:
            sheet = archive.read("xl/worksheets/sheet1.xml").decode("utf-8")
            table = archive.read("xl/tables/table1.xml").decode("utf-8")
        self.assertIn(fixtures.XLSX_SENTINEL, sheet)
        self.assertIn('tableParts count="1"', sheet)
        self.assertIn('ref="A1:B3"', table)
        self.assertIn('name="NativeSentinelTable"', table)

    def test_png_contains_visible_sentinel_pixels(self):
        data = fixtures.png_fixture()
        self.assertTrue(data.startswith(b"\x89PNG\r\n\x1a\n"))
        position = 8
        width = height = None
        text = b""
        compressed = bytearray()
        while position < len(data):
            length = struct.unpack(">I", data[position : position + 4])[0]
            kind = data[position + 4 : position + 8]
            chunk = data[position + 8 : position + 8 + length]
            position += 12 + length
            if kind == b"IHDR":
                width, height = struct.unpack(">II", chunk[:8])
            elif kind == b"tEXt":
                text += chunk
            elif kind == b"IDAT":
                compressed.extend(chunk)
        self.assertEqual(width, 196)
        self.assertEqual(height, 36)
        self.assertIn(fixtures.PNG_SENTINEL.encode("ascii"), text)
        pixels = zlib.decompress(compressed)
        self.assertGreater(pixels.count(b"\x10\x10\x10"), 0)

    def test_unknown_extension_and_large_stage_fixture_are_explicit(self):
        filename, data = fixtures.unknown_extension_fixture()
        self.assertTrue(filename.endswith(".opaque"))
        self.assertIn(b"M365_NATIVE_UNKNOWN_EXTENSION_SENTINEL", data)
        self.assertEqual(fixtures.LARGE_STAGED_SIZE, (16 << 20) + 1)


if __name__ == "__main__":
    unittest.main()
