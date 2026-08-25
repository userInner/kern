package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/userInner/kern/internal/model"
	"github.com/userInner/kern/internal/operation"
	"github.com/userInner/kern/internal/tool"
	"github.com/userInner/kern/internal/workspace"
)

var changeSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["action", "path"],
  "properties": {
    "action": {"type": "string", "enum": ["write_file", "replace", "move_file"]},
	"path": {"type": "string", "description": "Workspace-relative file path; source path for move_file"},
	"destination_path": {"type": "string", "description": "Workspace-relative destination required for move_file"},
    "content": {"type": "string", "description": "Complete content for write_file"},
    "old": {"type": "string", "description": "Exactly one existing fragment for replace"},
    "new": {"type": "string", "description": "Replacement fragment"},
    "expected_sha256": {"type": "string", "description": "Required current hash when changing an existing file"}
  }
}`)

// Change provides hash-checked atomic workspace writes.
type Change struct {
	workspace *workspace.Workspace
}

// NewChange constructs the change tool.
func NewChange(workspace *workspace.Workspace) (*Change, error) {
	if workspace == nil {
		return nil, errors.New("tool: workspace is required")
	}
	return &Change{workspace: workspace}, nil
}

func (c *Change) Definition() model.ToolDefinition {
	return model.ToolDefinition{
		Name: "change",
		Description: "Create or atomically modify a workspace file. Existing files require the SHA-256 " +
			"returned by inspect, preventing stale or blind overwrites. Files can be moved only to an absent destination.",
		InputSchema: changeSchema,
	}
}

func (c *Change) Effect(input json.RawMessage) (operation.Effect, error) {
	_, err := decodeChange(input)
	return operation.EffectLocalWrite, err
}

func (c *Change) Execute(
	ctx context.Context,
	input json.RawMessage,
) (tool.Result, error) {
	request, err := decodeChange(input)
	if err != nil {
		return tool.Result{}, err
	}
	var change workspace.Change
	switch request.Action {
	case "write_file":
		change, err = c.workspace.WriteFile(
			ctx,
			request.Path,
			[]byte(request.Content),
			request.ExpectedSHA256,
		)
	case "replace":
		change, err = c.workspace.Replace(
			ctx,
			request.Path,
			request.Old,
			request.New,
			request.ExpectedSHA256,
		)
	case "move_file":
		change, err = c.workspace.MoveFile(
			ctx,
			request.Path,
			request.DestinationPath,
			request.ExpectedSHA256,
		)
	}
	if err != nil {
		return tool.Result{}, err
	}
	encoded, err := json.Marshal(change)
	if err != nil {
		return tool.Result{}, fmt.Errorf("encoding workspace change: %w", err)
	}
	result := tool.Result{Content: string(encoded)}
	if change.Diff != "" {
		result.Artifacts = append(result.Artifacts, tool.OutputArtifact{
			Name:      filepath.Base(change.Path) + ".diff",
			MediaType: "text/x-diff",
			Content:   []byte(change.Diff),
		})
	}
	return result, nil
}

// PrepareRecovery records only path and hashes, never file content, before the
// atomic write starts.
func (c *Change) PrepareRecovery(
	ctx context.Context,
	input json.RawMessage,
) (json.RawMessage, error) {
	request, err := decodeChange(input)
	if err != nil {
		return nil, err
	}
	var intent workspace.WriteIntent
	switch request.Action {
	case "write_file":
		intent, err = c.workspace.PrepareWrite(
			ctx,
			request.Path,
			[]byte(request.Content),
			request.ExpectedSHA256,
		)
	case "replace":
		intent, err = c.workspace.PrepareReplace(
			ctx,
			request.Path,
			request.Old,
			request.New,
			request.ExpectedSHA256,
		)
	case "move_file":
		moveIntent, moveErr := c.workspace.PrepareMove(
			ctx,
			request.Path,
			request.DestinationPath,
			request.ExpectedSHA256,
		)
		if moveErr != nil {
			return nil, moveErr
		}
		metadata, marshalErr := json.Marshal(moveIntent)
		if marshalErr != nil {
			return nil, fmt.Errorf("encoding file move recovery intent: %w", marshalErr)
		}
		return metadata, nil
	}
	if err != nil {
		return nil, err
	}
	metadata, err := json.Marshal(intent)
	if err != nil {
		return nil, fmt.Errorf("encoding file recovery intent: %w", err)
	}
	return metadata, nil
}

// Reconcile classifies an interrupted atomic write from its current file hash.
func (c *Change) Reconcile(
	ctx context.Context,
	input json.RawMessage,
	metadata json.RawMessage,
) (tool.Reconciliation, error) {
	request, err := decodeChange(input)
	if err != nil {
		return tool.Reconciliation{}, err
	}
	if request.Action == "move_file" {
		return c.reconcileMove(ctx, request, metadata)
	}
	var intent workspace.WriteIntent
	if err := json.Unmarshal(metadata, &intent); err != nil {
		return tool.Reconciliation{}, fmt.Errorf("decoding file recovery intent: %w", err)
	}
	if intent.Path == "" || len(intent.AfterSHA256) != sha256.Size*2 ||
		(intent.BeforeSHA256 != "" && len(intent.BeforeSHA256) != sha256.Size*2) ||
		intent.Path != filepath.ToSlash(filepath.Clean(request.Path)) ||
		!strings.EqualFold(intent.BeforeSHA256, request.ExpectedSHA256) {
		return tool.Reconciliation{}, errors.New("tool: file recovery intent does not match operation input")
	}
	if request.Action == "write_file" {
		digest := sha256.Sum256([]byte(request.Content))
		if !strings.EqualFold(intent.AfterSHA256, hex.EncodeToString(digest[:])) {
			return tool.Reconciliation{}, errors.New("tool: file recovery target hash does not match operation input")
		}
	}
	current, err := c.workspace.ReadFile(ctx, intent.Path)
	if errors.Is(err, fs.ErrNotExist) && intent.Created {
		return tool.Reconciliation{
			Disposition: tool.RecoveryNotExecuted,
			Summary:     "The target file does not exist, matching the pre-write state.",
		}, nil
	}
	if err != nil {
		return tool.Reconciliation{}, err
	}
	if strings.EqualFold(current.SHA256, intent.AfterSHA256) {
		return tool.Reconciliation{
			Disposition: tool.RecoverySucceeded,
			Summary:     "The current file hash matches the persisted target hash.",
		}, nil
	}
	if intent.BeforeSHA256 != "" && strings.EqualFold(current.SHA256, intent.BeforeSHA256) {
		return tool.Reconciliation{
			Disposition: tool.RecoveryNotExecuted,
			Summary:     "The current file hash still matches the pre-write hash.",
		}, nil
	}
	return tool.Reconciliation{
		Disposition: tool.RecoveryConflict,
		Summary:     "The current file hash matches neither the pre-write nor target hash.",
	}, nil
}

func (c *Change) reconcileMove(
	ctx context.Context,
	request changeInput,
	metadata json.RawMessage,
) (tool.Reconciliation, error) {
	var intent workspace.MoveIntent
	if err := json.Unmarshal(metadata, &intent); err != nil {
		return tool.Reconciliation{}, fmt.Errorf("decoding file move recovery intent: %w", err)
	}
	if intent.SourcePath == "" || intent.DestinationPath == "" || len(intent.SHA256) != sha256.Size*2 ||
		intent.SourcePath != filepath.ToSlash(filepath.Clean(request.Path)) ||
		intent.DestinationPath != filepath.ToSlash(filepath.Clean(request.DestinationPath)) ||
		!strings.EqualFold(intent.SHA256, request.ExpectedSHA256) {
		return tool.Reconciliation{}, errors.New("tool: file move recovery intent does not match operation input")
	}
	source, sourceErr := c.workspace.ReadFile(ctx, intent.SourcePath)
	destination, destinationErr := c.workspace.ReadFile(ctx, intent.DestinationPath)
	sourceMissing := errors.Is(sourceErr, fs.ErrNotExist)
	destinationMissing := errors.Is(destinationErr, fs.ErrNotExist)
	if sourceErr != nil && !sourceMissing {
		return tool.Reconciliation{}, sourceErr
	}
	if destinationErr != nil && !destinationMissing {
		return tool.Reconciliation{}, destinationErr
	}
	if !sourceMissing && destinationMissing && strings.EqualFold(source.SHA256, intent.SHA256) {
		return tool.Reconciliation{
			Disposition: tool.RecoveryNotExecuted,
			Summary:     "The source still has the approved hash and the destination is absent.",
		}, nil
	}
	if sourceMissing && !destinationMissing && strings.EqualFold(destination.SHA256, intent.SHA256) {
		return tool.Reconciliation{
			Disposition: tool.RecoverySucceeded,
			Summary:     "The source is absent and the destination has the approved hash.",
		}, nil
	}
	return tool.Reconciliation{
		Disposition: tool.RecoveryConflict,
		Summary:     "The source and destination do not match either complete move state.",
	}, nil
}

type changeInput struct {
	Action          string `json:"action"`
	Path            string `json:"path"`
	DestinationPath string `json:"destination_path"`
	Content         string `json:"content"`
	Old             string `json:"old"`
	New             string `json:"new"`
	ExpectedSHA256  string `json:"expected_sha256"`
}

func decodeChange(input json.RawMessage) (changeInput, error) {
	var request changeInput
	if err := decodeStrict(input, &request); err != nil {
		return changeInput{}, err
	}
	if request.Path == "" {
		return changeInput{}, fmt.Errorf("%w: path is required", tool.ErrInvalidInput)
	}
	switch request.Action {
	case "write_file":
	case "replace":
		if request.Old == "" {
			return changeInput{}, fmt.Errorf("%w: old text is required", tool.ErrInvalidInput)
		}
	case "move_file":
		if request.DestinationPath == "" || request.ExpectedSHA256 == "" {
			return changeInput{}, fmt.Errorf(
				"%w: destination_path and expected_sha256 are required for move_file",
				tool.ErrInvalidInput,
			)
		}
		if request.Content != "" || request.Old != "" || request.New != "" {
			return changeInput{}, fmt.Errorf("%w: move_file does not accept content edits", tool.ErrInvalidInput)
		}
	default:
		return changeInput{}, fmt.Errorf("%w: unsupported change action", tool.ErrInvalidInput)
	}
	return request, nil
}

var _ tool.Handler = (*Change)(nil)
var _ tool.RecoveryHandler = (*Change)(nil)
