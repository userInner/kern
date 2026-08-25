package ledger

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrIdempotencyConflict = errors.New("idempotency key conflict")
	ErrInvalidTransfer     = errors.New("invalid transfer")
	ErrInsufficientFunds   = errors.New("insufficient funds")
)

type Receipt struct {
	ID     string
	From   string
	To     string
	Amount int64
}

type Ledger struct {
	balances map[string]int64
	receipts map[string]Receipt
}

func NewLedger(balances map[string]int64) *Ledger {
	return &Ledger{balances: balances, receipts: make(map[string]Receipt)}
}

func (l *Ledger) Transfer(_ context.Context, key, from, to string, amount int64) (Receipt, error) {
	if key == "" || from == to || amount <= 0 {
		return Receipt{}, ErrInvalidTransfer
	}
	if receipt, ok := l.receipts[key]; ok {
		return receipt, nil
	}
	if l.balances[from] < amount {
		return Receipt{}, ErrInsufficientFunds
	}
	l.balances[from] -= amount
	l.balances[to] += amount
	receipt := Receipt{ID: fmt.Sprintf("receipt-%s", key), From: from, To: to, Amount: amount}
	l.receipts[key] = receipt
	return receipt, nil
}

func (l *Ledger) Balance(account string) int64 { return l.balances[account] }
