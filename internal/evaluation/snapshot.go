package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
)

// Snapshot copies every selected suite input into a private, run-owned
// directory and returns a suite backed by a stable capability to that copy.
// The returned Suite owns the capability and must be closed when execution is
// complete.
func Snapshot(ctx context.Context, suite Suite, destination string) (result Suite, err error) {
	if err := Validate(suite); err != nil {
		return Suite{}, err
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		if statErr == nil {
			return Suite{}, errors.New("evaluation: snapshot destination already exists")
		}
		return Suite{}, fmt.Errorf("evaluation: checking snapshot destination: %w", statErr)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return Suite{}, fmt.Errorf("evaluation: creating input snapshot: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			err = errors.Join(err, os.RemoveAll(destination))
		}
	}()
	root, err := os.OpenRoot(destination)
	if err != nil {
		return Suite{}, fmt.Errorf("evaluation: opening input snapshot: %w", err)
	}
	scope := &suiteRoot{root: root, owned: true}
	defer func() {
		if !complete {
			err = errors.Join(err, scope.close())
		}
	}()

	snapshot := suite
	snapshot.Root = ""
	snapshot.rootAnchor = ""
	snapshot.rootRelative = ""
	snapshot.rootScope = nil
	snapshot.Cases = append([]Case(nil), suite.Cases...)
	for index, evalCase := range suite.Cases {
		if err := ctx.Err(); err != nil {
			return Suite{}, err
		}
		caseRoot := path.Join("cases", evalCase.ID)
		promptName := path.Join(caseRoot, "prompt.txt")
		fixtureName := path.Join(caseRoot, "fixture")
		prompt, err := suite.PromptText(evalCase)
		if err != nil {
			return Suite{}, err
		}
		fixture, err := suite.openFixture(evalCase)
		if err != nil {
			return Suite{}, err
		}
		if err := root.MkdirAll(filepath.FromSlash(caseRoot), 0o700); err != nil {
			_ = fixture.Close()
			return Suite{}, fmt.Errorf("evaluation: creating snapshot case directory: %w", err)
		}
		if err := root.WriteFile(
			filepath.FromSlash(promptName),
			[]byte(prompt),
			0o600,
		); err != nil {
			_ = fixture.Close()
			return Suite{}, fmt.Errorf("evaluation: writing snapshot prompt: %w", err)
		}
		copyErr := copyFixtureToRoot(ctx, fixture, root, filepath.FromSlash(fixtureName))
		closeErr := fixture.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return Suite{}, fmt.Errorf("evaluation: snapshotting fixture: %w", err)
		}
		snapshot.Cases[index].Prompt = promptName
		snapshot.Cases[index].Fixture = fixtureName
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return Suite{}, fmt.Errorf("evaluation: encoding snapshot suite: %w", err)
	}
	if err := root.WriteFile("suite.json", data, 0o600); err != nil {
		return Suite{}, fmt.Errorf("evaluation: writing snapshot suite: %w", err)
	}
	loaded, err := loadFromRoot(scope, "suite.json")
	if err != nil {
		return Suite{}, err
	}
	complete = true
	return loaded, nil
}
