package ledger

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestTransferIsConcurrentAndIdempotent(t *testing.T) {
	ledger := NewLedger(map[string]int64{"alice": 1000, "bob": 0})
	const workers = 32
	receipts := make(chan Receipt, workers)
	errorsSeen := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			receipt, err := ledger.Transfer(context.Background(), "same-key", "alice", "bob", 100)
			receipts <- receipt
			errorsSeen <- err
		}()
	}
	group.Wait()
	close(receipts)
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil { t.Fatalf("Transfer() error = %v", err) }
	}
	var first Receipt
	for receipt := range receipts {
		if first.ID == "" { first = receipt }
		if receipt != first { t.Fatalf("receipt = %#v, want %#v", receipt, first) }
	}
	if got := ledger.Balance("alice"); got != 900 { t.Fatalf("alice balance = %d", got) }
	if got := ledger.Balance("bob"); got != 100 { t.Fatalf("bob balance = %d", got) }
}

func TestTransferRejectsConflictingReuseAndInvalidRequests(t *testing.T) {
	ledger := NewLedger(map[string]int64{"alice": 500, "bob": 0, "carol": 0})
	if _, err := ledger.Transfer(context.Background(), "key", "alice", "bob", 100); err != nil { t.Fatal(err) }
	if _, err := ledger.Transfer(context.Background(), "key", "alice", "carol", 100); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting reuse error = %v", err)
	}
	for _, call := range []struct{ key, from, to string; amount int64 }{
		{"", "alice", "bob", 1}, {"bad", "alice", "alice", 1}, {"bad", "alice", "bob", 0}, {"bad", "alice", "bob", 1000},
	} {
		_, _ = ledger.Transfer(context.Background(), call.key, call.from, call.to, call.amount)
	}
	if ledger.Balance("alice") != 400 || ledger.Balance("bob") != 100 || ledger.Balance("carol") != 0 {
		t.Fatalf("invalid requests changed balances")
	}
}
