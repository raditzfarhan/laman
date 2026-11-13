package daemon

import (
	"context"
	"testing"
	"time"
)

// A slow fetch must not hold up cached(), which refresh calls.
func TestLinesCachedDoesNotWaitForFetch(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	l := &lines{fetch: func(ctx context.Context) ([]string, error) {
		close(started)
		<-release
		return []string{"8.4", "8.5"}, nil
	}}
	got := make(chan []string)
	go func() { list, _ := l.get(); got <- list }()
	<-started

	done := make(chan []string)
	go func() { done <- l.cached() }()
	select {
	case c := <-done:
		if c != nil {
			t.Errorf("cached() = %v before the fetch finished, want nil", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cached() waited for the fetch")
	}

	close(release)
	if list := <-got; len(list) != 2 {
		t.Fatalf("get() = %v", list)
	}
	if c := l.cached(); len(c) != 2 {
		t.Errorf("cached() = %v after the fetch", c)
	}
}

func TestLinesFetchHasDeadline(t *testing.T) {
	l := &lines{fetch: func(ctx context.Context) ([]string, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("the fetch has no deadline")
		}
		return []string{"8.5"}, nil
	}}
	if _, err := l.get(); err != nil {
		t.Fatal(err)
	}
	// Fresh lines are not fetched again.
	l.fetch = func(context.Context) ([]string, error) { t.Error("fetched again"); return nil, nil }
	l.get()
}
