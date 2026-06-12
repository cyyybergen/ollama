// Package docfiles provides reading and writing of common document formats
// (csv, xlsx, ods, docx, odt) used by the desktop app to extract attachment
// text for models and to let models edit or create document files.
package docfiles

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

const (
	// maxRowsPerSheet caps how many rows of a spreadsheet are rendered to
	// text so very large files don't blow up the model's context window.
	maxRowsPerSheet = 500

	// maxOutputBytes caps the total text produced by extraction.
	maxOutputBytes = 200 * 1024
)

// DocumentExtensions are the file extensions handled by ExtractText.
var DocumentExtensions = []string{".csv", ".xlsx", ".ods", ".docx", ".odt"}

// IsDocument reports whether the filename has an extension handled by this package.
func IsDocument(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	for _, e := range DocumentExtensions {
		if ext == e {
			return true
		}
	}
	return false
}

// ExtractText converts document bytes to text for use as model context.
// The boolean result reports whether the file type was handled.
func ExtractText(data []byte, filename string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(filename))

	var text string
	var err error
	switch ext {
	case ".csv":
		text, err = extractCSV(data)
	case ".xlsx":
		text, err = extractXLSX(data)
	case ".ods":
		text, err = extractODS(data)
	case ".docx":
		text, err = extractDOCX(data)
	case ".odt":
		text, err = extractODT(data)
	default:
		return "", false
	}

	if err != nil {
		return fmt.Sprintf("[%s file - %d bytes - failed to extract text: %v]", ext, len(data), err), true
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Sprintf("[%s file - %d bytes - no text content found]", ext, len(data)), true
	}
	return truncateText(text), true
}

func truncateText(text string) string {
	if len(text) <= maxOutputBytes {
		return text
	}
	cut := text[:maxOutputBytes]
	// avoid splitting a UTF-8 rune
	for len(cut) > 0 && cut[len(cut)-1]&0xC0 == 0x80 {
		cut = cut[:len(cut)-1]
	}
	return cut + "\n[... truncated: file content exceeds size limit ...]"
}

// extractCSV parses CSV bytes (handling BOM and common delimiters) and
// renders normalized comma-separated output.
func extractCSV(data []byte) (string, error) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})

	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = sniffDelimiter(data)
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	var rows [][]string
	truncated := false
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("invalid CSV: %w", err)
		}
		if len(rows) >= maxRowsPerSheet {
			truncated = true
			break
		}
		rows = append(rows, record)
	}

	out, err := renderCSV(rows)
	if err != nil {
		return "", err
	}
	if truncated {
		out += fmt.Sprintf("[... truncated: only the first %d rows are shown ...]\n", maxRowsPerSheet)
	}
	return out, nil
}

// sniffDelimiter guesses the delimiter of CSV-like data by counting
// candidate separators on the first line.
func sniffDelimiter(data []byte) rune {
	line := data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		line = data[:i]
	}
	best, bestCount := ',', bytes.Count(line, []byte{','})
	for _, c := range []byte{';', '\t'} {
		if n := bytes.Count(line, []byte{c}); n > bestCount {
			best, bestCount = rune(c), n
		}
	}
	return best
}

func renderCSV(rows [][]string) (string, error) {
	var sb strings.Builder
	w := csv.NewWriter(&sb)
	for _, row := range rows {
		if len(row) == 0 {
			sb.WriteString("\n")
			continue
		}
		if err := w.Write(row); err != nil {
			return "", err
		}
		w.Flush()
	}
	return sb.String(), w.Error()
}

// extractXLSX renders each sheet of an xlsx workbook as CSV prefixed by the
// sheet name.
func extractXLSX(data []byte) (string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	defer f.Close()

	var sb strings.Builder
	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			return "", fmt.Errorf("sheet %q: %w", sheet, err)
		}
		writeSheet(&sb, sheet, rows)
	}
	return sb.String(), nil
}

func writeSheet(sb *strings.Builder, name string, rows [][]string) {
	if sb.Len() > 0 {
		sb.WriteString("\n")
	}
	fmt.Fprintf(sb, "=== Sheet: %s ===\n", name)
	truncated := false
	if len(rows) > maxRowsPerSheet {
		rows = rows[:maxRowsPerSheet]
		truncated = true
	}
	if out, err := renderCSV(rows); err == nil {
		sb.WriteString(out)
	}
	if truncated {
		fmt.Fprintf(sb, "[... truncated: only the first %d rows are shown ...]\n", maxRowsPerSheet)
	}
}

// odfContent models the subset of an OpenDocument content.xml needed for
// text extraction from .ods and .odt files.
type odfContent struct {
	Body struct {
		Spreadsheet struct {
			Tables []odfTable `xml:"table"`
		} `xml:"spreadsheet"`
		Text struct {
			Paragraphs []odfParagraph `xml:",any"`
		} `xml:"text"`
	} `xml:"body"`
}

type odfTable struct {
	Name string   `xml:"name,attr"`
	Rows []odfRow `xml:"table-row"`
}

type odfRow struct {
	Repeated int       `xml:"number-rows-repeated,attr"`
	Cells    []odfCell `xml:"table-cell"`
}

type odfCell struct {
	Repeated   int      `xml:"number-columns-repeated,attr"`
	Paragraphs []string `xml:"p"`
}

type odfParagraph struct {
	XMLName xml.Name
	Text    string `xml:",chardata"`
}

func readZipEntry(data []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("%s not found in archive", name)
}

// extractODS renders each sheet of an OpenDocument spreadsheet as CSV.
func extractODS(data []byte) (string, error) {
	content, err := readZipEntry(data, "content.xml")
	if err != nil {
		return "", err
	}
	var doc odfContent
	if err := xml.Unmarshal(content, &doc); err != nil {
		return "", err
	}

	var sb strings.Builder
	for _, table := range doc.Body.Spreadsheet.Tables {
		var rows [][]string
		for _, row := range table.Rows {
			var cells []string
			for _, cell := range row.Cells {
				value := strings.Join(cell.Paragraphs, "\n")
				repeat := max(cell.Repeated, 1)
				// ignore huge trailing repeats of empty cells
				if value == "" && repeat > 1 {
					repeat = 1
				}
				for range repeat {
					cells = append(cells, value)
				}
			}
			// trim trailing empty cells
			for len(cells) > 0 && cells[len(cells)-1] == "" {
				cells = cells[:len(cells)-1]
			}
			repeat := max(row.Repeated, 1)
			if len(cells) == 0 && repeat > 1 {
				repeat = 1
			}
			for range repeat {
				rows = append(rows, cells)
			}
		}
		// trim trailing empty rows
		for len(rows) > 0 && len(rows[len(rows)-1]) == 0 {
			rows = rows[:len(rows)-1]
		}
		writeSheet(&sb, table.Name, rows)
	}
	return sb.String(), nil
}

// extractODT extracts paragraph text from an OpenDocument text file.
func extractODT(data []byte) (string, error) {
	content, err := readZipEntry(data, "content.xml")
	if err != nil {
		return "", err
	}
	var doc odfContent
	if err := xml.Unmarshal(content, &doc); err != nil {
		return "", err
	}

	var lines []string
	for _, p := range doc.Body.Text.Paragraphs {
		switch p.XMLName.Local {
		case "p", "h":
			lines = append(lines, strings.TrimSpace(p.Text))
		}
	}
	return strings.Join(lines, "\n"), nil
}

// docxDocument models the subset of word/document.xml needed for text extraction.
type docxDocument struct {
	Body struct {
		Paragraphs []docxParagraph `xml:"p"`
	} `xml:"body"`
}

type docxParagraph struct {
	Runs []struct {
		Text string `xml:"t"`
	} `xml:"r"`
}

// extractDOCX extracts paragraph text from a Word document.
func extractDOCX(data []byte) (string, error) {
	content, err := readZipEntry(data, "word/document.xml")
	if err != nil {
		return "", err
	}
	var doc docxDocument
	if err := xml.Unmarshal(content, &doc); err != nil {
		return "", err
	}

	var lines []string
	for _, p := range doc.Body.Paragraphs {
		var sb strings.Builder
		for _, r := range p.Runs {
			sb.WriteString(r.Text)
		}
		lines = append(lines, sb.String())
	}
	return strings.Join(lines, "\n"), nil
}

// SafeJoin joins dir and filename, rejecting names that could escape dir.
// Only plain file names (no directory components) are allowed.
func SafeJoin(dir, filename string) (string, error) {
	if filename == "" {
		return "", errors.New("filename is required")
	}
	if filename != filepath.Base(filename) || !filepath.IsLocal(filename) ||
		strings.ContainsAny(filename, `/\`) || strings.Contains(filename, "..") {
		return "", fmt.Errorf("invalid filename %q: must be a plain file name", filename)
	}
	return filepath.Join(dir, filename), nil
}

// CellEdit is a single spreadsheet cell assignment, e.g. {Cell: "B2", Value: "42"}.
type CellEdit struct {
	Cell  string
	Value string
}

// EditSpreadsheet applies cell edits to the named sheet of an xlsx file at
// path, creating the file and/or sheet if they don't exist.
func EditSpreadsheet(path, sheet string, edits []CellEdit) error {
	if strings.ToLower(filepath.Ext(path)) != ".xlsx" {
		return errors.New("only .xlsx files can be edited; write other formats as .csv or create a new .xlsx file")
	}
	if sheet == "" {
		sheet = "Sheet1"
	}

	var f *excelize.File
	if _, err := os.Stat(path); err == nil {
		f, err = excelize.OpenFile(path)
		if err != nil {
			return fmt.Errorf("failed to open spreadsheet: %w", err)
		}
	} else {
		f = excelize.NewFile()
		if sheet != "Sheet1" {
			if err := f.SetSheetName("Sheet1", sheet); err != nil {
				return err
			}
		}
	}
	defer f.Close()

	if idx, err := f.GetSheetIndex(sheet); err != nil {
		return err
	} else if idx < 0 {
		if _, err := f.NewSheet(sheet); err != nil {
			return err
		}
	}

	for _, edit := range edits {
		if _, _, err := excelize.CellNameToCoordinates(edit.Cell); err != nil {
			return fmt.Errorf("invalid cell reference %q: %w", edit.Cell, err)
		}
		var value any = edit.Value
		if n, err := strconv.ParseFloat(edit.Value, 64); err == nil {
			value = n
		}
		if err := f.SetCellValue(sheet, edit.Cell, value); err != nil {
			return fmt.Errorf("failed to set cell %s: %w", edit.Cell, err)
		}
	}

	return f.SaveAs(path)
}

// WriteCSVFile validates that content parses as CSV and writes it to path.
func WriteCSVFile(path, content string) error {
	if strings.ToLower(filepath.Ext(path)) != ".csv" {
		return errors.New("filename must end in .csv")
	}
	reader := csv.NewReader(strings.NewReader(content))
	reader.FieldsPerRecord = -1
	if _, err := reader.ReadAll(); err != nil {
		return fmt.Errorf("content is not valid CSV: %w", err)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// WriteDocument writes text content to path. Supported extensions are .docx
// (each line becomes a paragraph), .txt and .md (written verbatim).
func WriteDocument(path, content string) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".docx":
		data, err := buildDocx(strings.Split(content, "\n"))
		if err != nil {
			return err
		}
		return os.WriteFile(path, data, 0o644)
	case ".txt", ".md":
		return os.WriteFile(path, []byte(content), 0o644)
	default:
		return errors.New("filename must end in .docx, .txt or .md")
	}
}

// buildDocx produces a minimal valid Word document with the given paragraphs.
func buildDocx(paragraphs []string) ([]byte, error) {
	var doc strings.Builder
	doc.WriteString(xml.Header)
	doc.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paragraphs {
		var text bytes.Buffer
		if err := xml.EscapeText(&text, []byte(p)); err != nil {
			return nil, err
		}
		doc.WriteString(`<w:p><w:r><w:t xml:space="preserve">`)
		doc.Write(text.Bytes())
		doc.WriteString(`</w:t></w:r></w:p>`)
	}
	doc.WriteString(`</w:body></w:document>`)

	files := map[string]string{
		"[Content_Types].xml": xml.Header + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         xml.Header + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":   doc.String(),
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml"} {
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
