//go:build windows || darwin

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ollama/ollama/app/docfiles"
)

// DocumentArtifact describes a file produced or modified by a document tool.
// It is persisted as the tool result and rendered by the UI as an openable file.
type DocumentArtifact struct {
	Filename string `json:"filename"`
	Action   string `json:"action"`
}

// documentTool holds the shared per-chat workspace directory.
type documentTool struct {
	workspaceDir string
}

func (d *documentTool) resolve(args map[string]any) (path, filename string, err error) {
	filename, _ = args["filename"].(string)
	path, err = docfiles.SafeJoin(d.workspaceDir, filename)
	if err != nil {
		return "", "", err
	}
	return path, filename, nil
}

// NewDocumentTools returns the set of document tools rooted at workspaceDir.
func NewDocumentTools(workspaceDir string) []Tool {
	base := documentTool{workspaceDir: workspaceDir}
	return []Tool{
		&ListFiles{base},
		&ReadDocument{base},
		&EditSpreadsheet{base},
		&WriteCSV{base},
		&WriteDocument{base},
	}
}

func mustSchema(s string) map[string]any {
	var schema map[string]any
	if err := json.Unmarshal([]byte(s), &schema); err != nil {
		return nil
	}
	return schema
}

// ListFiles lists the files in the chat workspace.
type ListFiles struct{ documentTool }

func (t *ListFiles) Name() string { return "list_files" }
func (t *ListFiles) Description() string {
	return "List the files available in the chat workspace, including the user's attached documents and any files created by tools."
}
func (t *ListFiles) Prompt() string { return "" }
func (t *ListFiles) Schema() map[string]any {
	return mustSchema(`{"type": "object", "properties": {}}`)
}

func (t *ListFiles) Execute(ctx context.Context, args map[string]any) (any, string, error) {
	entries, err := os.ReadDir(t.workspaceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "The workspace is empty.", nil
		}
		return nil, "", err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, "The workspace is empty.", nil
	}
	return nil, "Files in workspace:\n" + strings.Join(names, "\n"), nil
}

// ReadDocument extracts the text content of a workspace file.
type ReadDocument struct{ documentTool }

func (t *ReadDocument) Name() string { return "read_document" }
func (t *ReadDocument) Description() string {
	return "Read a file from the chat workspace. Spreadsheets (.xlsx, .ods, .csv) are returned as CSV text per sheet; documents (.docx, .odt) are returned as plain text."
}
func (t *ReadDocument) Prompt() string { return "" }
func (t *ReadDocument) Schema() map[string]any {
	return mustSchema(`{
		"type": "object",
		"properties": {
			"filename": {
				"type": "string",
				"description": "Name of the file in the workspace, e.g. invoices.xlsx"
			}
		},
		"required": ["filename"]
	}`)
}

func (t *ReadDocument) Execute(ctx context.Context, args map[string]any) (any, string, error) {
	path, filename, err := t.resolve(args)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read %s: %w", filename, err)
	}
	if text, handled := docfiles.ExtractText(data, filename); handled {
		return nil, text, nil
	}
	return nil, string(data), nil
}

// EditSpreadsheet sets cell values in an xlsx file in the workspace.
type EditSpreadsheet struct{ documentTool }

func (t *EditSpreadsheet) Name() string { return "edit_spreadsheet" }
func (t *EditSpreadsheet) Description() string {
	return "Set cell values in an .xlsx spreadsheet in the chat workspace. Creates the file and sheet if they don't exist. Use read_document afterwards to verify the result."
}
func (t *EditSpreadsheet) Prompt() string { return "" }
func (t *EditSpreadsheet) Schema() map[string]any {
	return mustSchema(`{
		"type": "object",
		"properties": {
			"filename": {
				"type": "string",
				"description": "Name of the .xlsx file in the workspace, e.g. invoices.xlsx"
			},
			"sheet": {
				"type": "string",
				"description": "Sheet name to edit (default: Sheet1)"
			},
			"edits": {
				"type": "array",
				"description": "Cell assignments to apply, e.g. [{\"cell\": \"B2\", \"value\": \"42\"}]. Numeric values are stored as numbers.",
				"items": {
					"type": "object",
					"properties": {
						"cell": {"type": "string", "description": "Cell reference, e.g. A1"},
						"value": {"type": "string", "description": "Value to set"}
					},
					"required": ["cell", "value"]
				}
			}
		},
		"required": ["filename", "edits"]
	}`)
}

func (t *EditSpreadsheet) Execute(ctx context.Context, args map[string]any) (any, string, error) {
	path, filename, err := t.resolve(args)
	if err != nil {
		return nil, "", err
	}

	sheet, _ := args["sheet"].(string)

	rawEdits, ok := args["edits"].([]any)
	if !ok || len(rawEdits) == 0 {
		return nil, "", fmt.Errorf("edits parameter is required and must be a non-empty array")
	}
	edits := make([]docfiles.CellEdit, 0, len(rawEdits))
	for _, raw := range rawEdits {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("each edit must be an object with cell and value")
		}
		cell, _ := m["cell"].(string)
		value := fmt.Sprintf("%v", m["value"])
		if cell == "" {
			return nil, "", fmt.Errorf("each edit must include a cell reference")
		}
		edits = append(edits, docfiles.CellEdit{Cell: cell, Value: value})
	}

	if err := docfiles.EditSpreadsheet(path, sheet, edits); err != nil {
		return nil, "", err
	}

	artifact := DocumentArtifact{Filename: filename, Action: "edited"}
	return artifact, fmt.Sprintf("Applied %d edit(s) to %s.", len(edits), filename), nil
}

// WriteCSV writes CSV content to a file in the workspace.
type WriteCSV struct{ documentTool }

func (t *WriteCSV) Name() string { return "write_csv" }
func (t *WriteCSV) Description() string {
	return "Write CSV content to a .csv file in the chat workspace, creating or overwriting it."
}
func (t *WriteCSV) Prompt() string { return "" }
func (t *WriteCSV) Schema() map[string]any {
	return mustSchema(`{
		"type": "object",
		"properties": {
			"filename": {
				"type": "string",
				"description": "Name of the .csv file to write, e.g. summary.csv"
			},
			"content": {
				"type": "string",
				"description": "The full CSV content to write"
			}
		},
		"required": ["filename", "content"]
	}`)
}

func (t *WriteCSV) Execute(ctx context.Context, args map[string]any) (any, string, error) {
	path, filename, err := t.resolve(args)
	if err != nil {
		return nil, "", err
	}
	content, _ := args["content"].(string)
	if err := docfiles.WriteCSVFile(path, content); err != nil {
		return nil, "", err
	}
	artifact := DocumentArtifact{Filename: filename, Action: "written"}
	return artifact, fmt.Sprintf("Wrote %s.", filename), nil
}

// WriteDocument writes text content to a .docx, .txt or .md file in the workspace.
type WriteDocument struct{ documentTool }

func (t *WriteDocument) Name() string { return "write_document" }
func (t *WriteDocument) Description() string {
	return "Write text content to a .docx, .txt or .md file in the chat workspace, creating or overwriting it. For .docx, each line of content becomes a paragraph."
}
func (t *WriteDocument) Prompt() string { return "" }
func (t *WriteDocument) Schema() map[string]any {
	return mustSchema(`{
		"type": "object",
		"properties": {
			"filename": {
				"type": "string",
				"description": "Name of the file to write, e.g. report.docx"
			},
			"content": {
				"type": "string",
				"description": "The full text content to write"
			}
		},
		"required": ["filename", "content"]
	}`)
}

func (t *WriteDocument) Execute(ctx context.Context, args map[string]any) (any, string, error) {
	path, filename, err := t.resolve(args)
	if err != nil {
		return nil, "", err
	}
	content, _ := args["content"].(string)
	if err := docfiles.WriteDocument(path, content); err != nil {
		return nil, "", err
	}
	artifact := DocumentArtifact{Filename: filename, Action: "written"}
	return artifact, fmt.Sprintf("Wrote %s.", filename), nil
}
