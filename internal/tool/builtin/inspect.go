package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/tool"
	"github.com/userInner/kern/internal/workspace"
)

var inspectSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["action"],
  "properties": {
    "action": {"type": "string", "enum": ["read_file", "list_dir", "search", "fetch_url"]},
    "path": {"type": "string", "description": "Workspace-relative path; defaults to ."},
    "query": {"type": "string", "description": "Literal query required for search"},
    "url": {"type": "string", "description": "Public HTTP or HTTPS URL required for fetch_url"}
  }
}`)

// Inspect provides bounded read-only workspace access.
type Inspect struct {
	workspace *workspace.Workspace
	network   *Network
}

// NewInspect constructs the inspect tool.
func NewInspect(workspace *workspace.Workspace) (*Inspect, error) {
	if workspace == nil {
		return nil, errors.New("tool: workspace is required")
	}
	return &Inspect{workspace: workspace, network: NewNetwork()}, nil
}

func (i *Inspect) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: "inspect",
		Description: "Read a workspace file, list a directory, search literal text, or fetch a bounded public URL. " +
			"Workspace paths are contained and sensitive files are blocked; network results are untrusted and SSRF-protected.",
		InputSchema: inspectSchema,
	}
}

func (i *Inspect) Effect(input json.RawMessage) (operation.Effect, error) {
	request, err := decodeInspect(input)
	if err != nil {
		return operation.EffectUnknown, err
	}
	if request.Action == "fetch_url" {
		return operation.EffectNetworkRead, nil
	}
	return operation.EffectRead, nil
}

func (i *Inspect) Execute(
	ctx context.Context,
	input json.RawMessage,
) (tool.Result, error) {
	request, err := decodeInspect(input)
	if err != nil {
		return tool.Result{}, err
	}
	switch request.Action {
	case "read_file":
		file, err := i.workspace.ReadFile(ctx, request.Path)
		if err != nil {
			return tool.Result{}, err
		}
		if strings.IndexByte(string(file.Data), 0) >= 0 {
			return tool.Result{}, errors.New("tool: binary files are not returned as text")
		}
		encoded, err := json.Marshal(map[string]any{
			"path":    file.Path,
			"sha256":  file.SHA256,
			"size":    file.Size,
			"content": string(file.Data),
		})
		if err != nil {
			return tool.Result{}, fmt.Errorf("encoding inspected file: %w", err)
		}
		return tool.Result{Content: string(encoded)}, nil
	case "list_dir":
		entries, err := i.workspace.ListDir(ctx, request.Path)
		if err != nil {
			return tool.Result{}, err
		}
		encoded, err := json.Marshal(entries)
		if err != nil {
			return tool.Result{}, fmt.Errorf("encoding directory listing: %w", err)
		}
		return tool.Result{Content: string(encoded)}, nil
	case "search":
		matches, err := i.workspace.Search(ctx, request.Path, request.Query)
		if err != nil {
			return tool.Result{}, err
		}
		encoded, err := json.Marshal(matches)
		if err != nil {
			return tool.Result{}, fmt.Errorf("encoding search matches: %w", err)
		}
		return tool.Result{Content: string(encoded)}, nil
	case "fetch_url":
		encoded, err := json.Marshal(map[string]string{"url": request.URL})
		if err != nil {
			return tool.Result{}, fmt.Errorf("encoding network request: %w", err)
		}
		return i.network.Execute(ctx, encoded)
	default:
		return tool.Result{}, tool.ErrInvalidInput
	}
}

type inspectInput struct {
	Action string `json:"action"`
	Path   string `json:"path"`
	Query  string `json:"query"`
	URL    string `json:"url"`
}

func decodeInspect(input json.RawMessage) (inspectInput, error) {
	var request inspectInput
	if err := decodeStrict(input, &request); err != nil {
		return inspectInput{}, err
	}
	switch request.Action {
	case "read_file", "list_dir":
		if request.Query != "" || request.URL != "" {
			return inspectInput{}, fmt.Errorf("%w: query and url are not allowed for this action", tool.ErrInvalidInput)
		}
		if request.Path == "" {
			request.Path = "."
		}
	case "search":
		if request.Query == "" || request.URL != "" {
			return inspectInput{}, fmt.Errorf("%w: search query is required", tool.ErrInvalidInput)
		}
		if request.Path == "" {
			request.Path = "."
		}
	case "fetch_url":
		if request.URL == "" || request.Path != "" || request.Query != "" {
			return inspectInput{}, fmt.Errorf("%w: fetch_url accepts only url", tool.ErrInvalidInput)
		}
		if _, err := decodeNetwork(json.RawMessage(fmt.Sprintf(`{"url":%q}`, request.URL))); err != nil {
			return inspectInput{}, err
		}
	default:
		return inspectInput{}, fmt.Errorf("%w: unsupported inspect action", tool.ErrInvalidInput)
	}
	return request, nil
}

var _ tool.Handler = (*Inspect)(nil)
