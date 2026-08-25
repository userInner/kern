package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetCoalescesConcurrentLoadsAndCachesSuccess(t *testing.T) {
	cache := New()
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	loader := func(context.Context) (string, error) {
		if calls.Add(1) == 1 { close(started) }
		<-release
		return "value", nil
	}
	const workers = 24
	var group sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		group.Add(1)
		go func() { defer group.Done(); value, err := cache.Get(context.Background(), "key", loader); if err != nil || value != "value" { errs <- errors.New("unexpected result") } }()
	}
	<-started
	close(release)
	group.Wait()
	close(errs)
	for err := range errs { t.Fatal(err) }
	if calls.Load() != 1 { t.Fatalf("loader calls = %d, want 1", calls.Load()) }
	if value, err := cache.Get(context.Background(), "key", loader); err != nil || value != "value" || calls.Load() != 1 { t.Fatalf("cached Get() = %q, %v, calls %d", value, err, calls.Load()) }
}

func TestGetDoesNotCacheFailuresAndLoadsDifferentKeysIndependently(t *testing.T) {
	cache := New()
	var attempts atomic.Int32
	loader := func(context.Context) (string, error) {
		if attempts.Add(1) == 1 { return "", errors.New("temporary") }
		return "recovered", nil
	}
	if _, err := cache.Get(context.Background(), "retry", loader); err == nil { t.Fatal("first Get() error = nil") }
	if value, err := cache.Get(context.Background(), "retry", loader); err != nil || value != "recovered" { t.Fatalf("second Get() = %q, %v", value, err) }

	leftStarted := make(chan struct{})
	leftRelease := make(chan struct{})
	go func() { _, _ = cache.Get(context.Background(), "left", func(context.Context) (string, error) { close(leftStarted); <-leftRelease; return "left", nil }) }()
	<-leftStarted
	rightDone := make(chan struct{})
	go func() { _, _ = cache.Get(context.Background(), "right", func(context.Context) (string, error) { return "right", nil }); close(rightDone) }()
	select { case <-rightDone: case <-time.After(time.Second): t.Fatal("different key was blocked") }
	close(leftRelease)
}

func TestWaitingCallerCanCancelWithoutCancellingSharedLoad(t *testing.T) {
	cache := New()
	started := make(chan struct{})
	release := make(chan struct{})
	leaderDone := make(chan error, 1)
	go func() { _, err := cache.Get(context.Background(), "key", func(context.Context) (string, error) { close(started); <-release; return "ok", nil }); leaderDone <- err }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() { _, err := cache.Get(ctx, "key", func(context.Context) (string, error) { return "wrong", nil }); waiterDone <- err }()
	cancel()
	select { case err := <-waiterDone: if !errors.Is(err, context.Canceled) { t.Fatalf("waiter error = %v", err) }; case <-time.After(time.Second): t.Fatal("cancelled waiter blocked") }
	close(release)
	if err := <-leaderDone; err != nil { t.Fatalf("leader error = %v", err) }
}
