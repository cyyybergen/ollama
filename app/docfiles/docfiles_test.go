package docfiles

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func xlsxBytes(t *testing.T, sheets map[string][][]any) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	first := true
	for name, rows := range sheets {
		if first {
			if err := f.SetSheetName("Sheet1", name); err != nil {
				t.Fatal(err)
			}
			first = false
		} else {
			if _, err := f.NewSheet(name); err != nil {
				t.Fatal(err)
			}
		}
		for i, row := range rows {
			cell, _ := excelize.CoordinatesToCellName(1, i+1)
			if err := f.SetSheetRow(name, cell, &row); err != nil {
				t.Fatal(err)
			}
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestIsDocument(t *testing.T) {
	for _, name := range []string{"a.csv", "b.XLSX", "c.docx", "d.odt", "e.ods"} {
		if !IsDocument(name) {
			t.Errorf("IsDocument(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"a.txt", "b.pdf", "c.png", "noext"} {
		if IsDocument(name) {
			t.Errorf("IsDocument(%q) = true, want false", name)
		}
	}
}

func TestExtractCSV(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{"comma", "a,b\n1,2\n", "a,b\n1,2\n"},
		{"semicolon", "a;b\n1;2\n", "a,b\n1,2\n"},
		{"tab", "a\tb\n1\t2\n", "a,b\n1,2\n"},
		{"bom", "\xEF\xBB\xBFa,b\n1,2\n", "a,b\n1,2\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, handled := ExtractText([]byte(tc.data), "test.csv")
			if !handled {
				t.Fatal("csv not handled")
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractCSVTruncates(t *testing.T) {
	var sb strings.Builder
	for range maxRowsPerSheet + 100 {
		sb.WriteString("a,b,c\n")
	}
	got, _ := ExtractText([]byte(sb.String()), "big.csv")
	if !strings.Contains(got, "truncated") {
		t.Error("expected truncation notice")
	}
	if n := strings.Count(got, "a,b,c\n"); n != maxRowsPerSheet {
		t.Errorf("got %d rows, want %d", n, maxRowsPerSheet)
	}
}

func TestExtractXLSX(t *testing.T) {
	data := xlsxBytes(t, map[string][][]any{
		"Invoices": {
			{"Invoice", "Amount"},
			{"INV-1", 100.5},
			{"INV-2", 200},
		},
	})
	got, handled := ExtractText(data, "invoices.xlsx")
	if !handled {
		t.Fatal("xlsx not handled")
	}
	for _, want := range []string{"=== Sheet: Invoices ===", "Invoice,Amount", "INV-1,100.5", "INV-2,200"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q in:\n%s", want, got)
		}
	}
}

func TestExtractDOCX(t *testing.T) {
	doc := `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Hello</w:t></w:r><w:r><w:t> world</w:t></w:r></w:p><w:p><w:r><w:t>Second paragraph</w:t></w:r></w:p></w:body></w:document>`
	data := zipBytes(t, map[string]string{"word/document.xml": doc})
	got, handled := ExtractText(data, "test.docx")
	if !handled {
		t.Fatal("docx not handled")
	}
	if got != "Hello world\nSecond paragraph" {
		t.Errorf("got %q", got)
	}
}

func TestExtractDOCXRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.docx")
	if err := WriteDocument(path, "First line\nSecond & <line>"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, handled := ExtractText(data, "out.docx")
	if !handled {
		t.Fatal("docx not handled")
	}
	if got != "First line\nSecond & <line>" {
		t.Errorf("got %q", got)
	}
}

func TestExtractODT(t *testing.T) {
	content := `<?xml version="1.0"?>
<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0">
  <office:body><office:text>
    <text:h>Title</text:h>
    <text:p>Body text</text:p>
  </office:text></office:body>
</office:document-content>`
	data := zipBytes(t, map[string]string{"content.xml": content})
	got, handled := ExtractText(data, "test.odt")
	if !handled {
		t.Fatal("odt not handled")
	}
	if got != "Title\nBody text" {
		t.Errorf("got %q", got)
	}
}

func TestExtractODS(t *testing.T) {
	content := `<?xml version="1.0"?>
<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0">
  <office:body><office:spreadsheet>
    <table:table table:name="Sheet1">
      <table:table-row><table:table-cell><text:p>Name</text:p></table:table-cell><table:table-cell><text:p>Total</text:p></table:table-cell></table:table-row>
      <table:table-row><table:table-cell><text:p>Acme</text:p></table:table-cell><table:table-cell><text:p>42</text:p></table:table-cell></table:table-row>
    </table:table>
  </office:spreadsheet></office:body>
</office:document-content>`
	data := zipBytes(t, map[string]string{"content.xml": content})
	got, handled := ExtractText(data, "test.ods")
	if !handled {
		t.Fatal("ods not handled")
	}
	for _, want := range []string{"=== Sheet: Sheet1 ===", "Name,Total", "Acme,42"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q in:\n%s", want, got)
		}
	}
}

func TestExtractTextUnhandled(t *testing.T) {
	if _, handled := ExtractText([]byte("hello"), "a.txt"); handled {
		t.Error("txt should not be handled")
	}
}

func TestExtractCorruptFile(t *testing.T) {
	got, handled := ExtractText([]byte("not a zip"), "bad.xlsx")
	if !handled {
		t.Fatal("xlsx should be handled")
	}
	if !strings.Contains(got, "failed to extract text") {
		t.Errorf("expected failure notice, got %q", got)
	}
}

func TestSafeJoin(t *testing.T) {
	dir := t.TempDir()
	if _, err := SafeJoin(dir, "good.csv"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	for _, bad := range []string{"", "../evil.csv", "a/b.csv", "..", "/etc/passwd", `..\evil.csv`} {
		if _, err := SafeJoin(dir, bad); err == nil {
			t.Errorf("SafeJoin(%q) should fail", bad)
		}
	}
}

func TestEditSpreadsheetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.xlsx")

	// create new file
	err := EditSpreadsheet(path, "Income", []CellEdit{
		{Cell: "A1", Value: "Invoice"},
		{Cell: "B1", Value: "Amount"},
		{Cell: "A2", Value: "INV-1"},
		{Cell: "B2", Value: "99.5"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// edit existing file
	if err := EditSpreadsheet(path, "Income", []CellEdit{{Cell: "B2", Value: "150"}}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ExtractText(data, "new.xlsx")
	for _, want := range []string{"=== Sheet: Income ===", "Invoice,Amount", "INV-1,150"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q in:\n%s", want, got)
		}
	}
}

func TestEditSpreadsheetValidation(t *testing.T) {
	dir := t.TempDir()
	if err := EditSpreadsheet(filepath.Join(dir, "a.csv"), "", nil); err == nil {
		t.Error("expected error for non-xlsx file")
	}
	if err := EditSpreadsheet(filepath.Join(dir, "a.xlsx"), "", []CellEdit{{Cell: "bogus", Value: "1"}}); err == nil {
		t.Error("expected error for invalid cell reference")
	}
}

func TestWriteCSVFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.csv")
	if err := WriteCSVFile(path, "a,b\n1,2\n"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "a,b\n1,2\n" {
		t.Errorf("got %q", data)
	}
	if err := WriteCSVFile(filepath.Join(dir, "bad.txt"), "a,b"); err == nil {
		t.Error("expected error for non-csv extension")
	}
	if err := WriteCSVFile(path, "\"unterminated\n"); err == nil {
		t.Error("expected error for invalid CSV")
	}
}

func TestWriteDocumentValidation(t *testing.T) {
	dir := t.TempDir()
	if err := WriteDocument(filepath.Join(dir, "a.exe"), "x"); err == nil {
		t.Error("expected error for unsupported extension")
	}
	if err := WriteDocument(filepath.Join(dir, "a.txt"), "plain text"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
