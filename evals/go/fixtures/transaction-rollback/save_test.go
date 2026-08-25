package save

import (
	"errors"
	"testing"
)

var errWrite = errors.New("write failed")

type fakeDB struct{ tx *fakeTx }

func (database fakeDB) Begin() (Tx, error) { return database.tx, nil }

type fakeTx struct {
	writeErr   error
	committed  bool
	rolledBack bool
}

func (tx *fakeTx) Write(string) error { return tx.writeErr }
func (tx *fakeTx) Commit() error {
	tx.committed = true
	return nil
}
func (tx *fakeTx) Rollback() error {
	tx.rolledBack = true
	return nil
}

func TestValueRollsBackWriteFailure(t *testing.T) {
	tx := &fakeTx{writeErr: errWrite}
	err := Value(fakeDB{tx: tx}, "value")
	if !errors.Is(err, errWrite) || !tx.rolledBack || tx.committed {
		t.Fatalf("Value() error=%v committed=%t rolledBack=%t", err, tx.committed, tx.rolledBack)
	}
}

func TestValueCommitsSuccess(t *testing.T) {
	tx := &fakeTx{}
	if err := Value(fakeDB{tx: tx}, "value"); err != nil || !tx.committed || tx.rolledBack {
		t.Fatalf("Value() error=%v committed=%t rolledBack=%t", err, tx.committed, tx.rolledBack)
	}
}
