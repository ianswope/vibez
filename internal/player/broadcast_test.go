package player

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// collectLogs reads log entries from ch until it has want of them or the read
// stalls, so a test failure reports what actually arrived.
func collectLogs(t *testing.T, ch <-chan State, want int) []string {
	t.Helper()
	var got []string
	for len(got) < want {
		select {
		case s := <-ch:
			got = append(got, s.Logs...)
		case <-time.After(2 * time.Second):
			return got
		}
	}
	return got
}

func TestBroadcastKeepsLogsPastAFullBuffer(t *testing.T) {
	var b Broadcast
	ch := b.Subscribe()

	const n = subChanSize * 4
	for i := range n {
		b.SendLog(State{}, fmt.Sprintf("line %d", i))
	}

	got := collectLogs(t, ch, n)
	if len(got) != n {
		t.Fatalf("got %d log entries, want %d", len(got), n)
	}
	for i, line := range got {
		if want := fmt.Sprintf("line %d", i); line != want {
			t.Fatalf("entry %d = %q, want %q", i, line, want)
		}
	}
}

// A log entry must arrive even when nothing follows it. The JS side polls state
// deduplicated, so an idle player pushes nothing that a backlog could ride out on.
func TestBroadcastDeliversLogsWithNoFurtherState(t *testing.T) {
	var b Broadcast
	ch := b.Subscribe()

	for i := range subChanSize * 3 {
		b.Send(State{Bitrate: i})
	}
	b.SendLog(State{Bitrate: 999}, "the last thing before it went quiet")

	got := collectLogs(t, ch, 1)
	if len(got) != 1 || got[0] != "the last thing before it went quiet" {
		t.Fatalf("got %v, want the final log entry", got)
	}
}

func TestBroadcastCoalescesStateOnlyUpdates(t *testing.T) {
	sub := &subscriber{ch: make(chan State, subChanSize), wake: make(chan struct{}, 1)}

	// No delivery goroutine, so everything stays queued.
	for i := range maxPendingQueue * 2 {
		sub.push(State{Bitrate: i})
	}

	sub.mu.Lock()
	defer sub.mu.Unlock()
	if len(sub.queue) != 1 {
		t.Fatalf("queued %d state-only updates, want 1", len(sub.queue))
	}
	if got := sub.queue[0].Bitrate; got != maxPendingQueue*2-1 {
		t.Fatalf("kept update %d, want the newest (%d)", got, maxPendingQueue*2-1)
	}
}

func TestBroadcastBacklogIsPerSubscriber(t *testing.T) {
	var b Broadcast
	fast := b.Subscribe()
	slow := b.Subscribe()

	const n = subChanSize * 3
	done := make(chan []string, 1)
	go func() {
		done <- collectLogs(t, fast, n)
	}()

	for i := range n {
		b.SendLog(State{}, fmt.Sprintf("line %d", i))
	}

	if got := <-done; len(got) != n {
		t.Fatalf("fast subscriber got %d entries, want %d", len(got), n)
	}
	// The subscriber that read nothing while the other kept up must still have
	// everything waiting for it.
	if got := collectLogs(t, slow, n); len(got) != n {
		t.Fatalf("slow subscriber got %d entries, want %d", len(got), n)
	}
}

func TestBroadcastBacklogIsCappedAndReportsTheLoss(t *testing.T) {
	sub := &subscriber{ch: make(chan State, subChanSize), wake: make(chan struct{}, 1)}

	const overflow = 50
	for i := range maxPendingQueue + overflow {
		sub.push(State{Logs: []string{fmt.Sprintf("line %d", i)}})
	}

	sub.mu.Lock()
	queued, dropped := len(sub.queue), sub.dropped
	sub.mu.Unlock()
	if queued != maxPendingQueue {
		t.Fatalf("queue holds %d entries, cap is %d", queued, maxPendingQueue)
	}
	if dropped != overflow {
		t.Fatalf("counted %d dropped entries, want %d", dropped, overflow)
	}

	go sub.deliver()
	got := collectLogs(t, sub.ch, 1)
	if !strings.HasPrefix(got[0], "[log] ") || !strings.HasSuffix(got[0], " earlier entries dropped") {
		t.Fatalf("first entry after an overflow = %q, want a dropped-entry note", got[0])
	}
}

func TestBroadcastNeverBlocksOnAnUnreadSubscriber(t *testing.T) {
	var b Broadcast
	_ = b.Subscribe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range maxPendingQueue * 2 {
			b.SendLog(State{}, fmt.Sprintf("line %d", i))
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Send blocked on a subscriber that is not reading")
	}
}
