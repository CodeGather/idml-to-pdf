import os
import tempfile
import unittest
import zipfile

from idml_to_pdf import convert_idml_to_pdf, extract_idml_text


def _write_sample_idml(path: str) -> None:
    story_one = """<?xml version="1.0" encoding="UTF-8"?>
<idPkg:Story xmlns:idPkg="http://ns.adobe.com/AdobeInDesign/idml/1.0/packaging">
  <Story>
    <ParagraphStyleRange>
      <CharacterStyleRange><Content>Hello (IDML)</Content></CharacterStyleRange>
    </ParagraphStyleRange>
  </Story>
</idPkg:Story>
"""
    story_two = """<?xml version="1.0" encoding="UTF-8"?>
<idPkg:Story xmlns:idPkg="http://ns.adobe.com/AdobeInDesign/idml/1.0/packaging">
  <Story>
    <ParagraphStyleRange>
      <CharacterStyleRange><Content>Second line \\ content</Content></CharacterStyleRange>
    </ParagraphStyleRange>
  </Story>
</idPkg:Story>
"""
    with zipfile.ZipFile(path, "w") as idml:
        idml.writestr("mimetype", "application/vnd.adobe.indesign-idml-package")
        idml.writestr("Stories/Story_u2.xml", story_two)
        idml.writestr("Stories/Story_u1.xml", story_one)


class IdmlToPdfTests(unittest.TestCase):
    def test_extract_idml_text_reads_story_content_in_sorted_order(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            idml_path = os.path.join(tmp_dir, "sample.idml")
            _write_sample_idml(idml_path)

            lines = extract_idml_text(idml_path)

            self.assertEqual(lines, ["Hello (IDML)", "Second line \\ content"])

    def test_convert_idml_to_pdf_writes_valid_pdf_header_and_text(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            idml_path = os.path.join(tmp_dir, "sample.idml")
            output_pdf = os.path.join(tmp_dir, "output.pdf")
            _write_sample_idml(idml_path)

            convert_idml_to_pdf(idml_path, output_pdf)

            with open(output_pdf, "rb") as generated_pdf:
                pdf_bytes = generated_pdf.read()

            self.assertTrue(pdf_bytes.startswith(b"%PDF-1.4"))
            self.assertIn(b"(Hello \\(IDML\\)) Tj", pdf_bytes)
            self.assertIn(b"(Second line \\\\ content) Tj", pdf_bytes)


if __name__ == "__main__":
    unittest.main()
