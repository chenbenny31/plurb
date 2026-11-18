package web

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// listen starts the hub's LISTEN loop on a channel unique to this test.
func (a *testApp) listen() {
	b := make([]byte, 4)
	rand.Read(b)
	channel := "test_" + hex.EncodeToString(b)
	a.srv.Store().SetNotifyChannel(channel)
	ctx, cancel := context.WithCancel(context.Background())
	a.t.Cleanup(cancel)
	go a.srv.hub.Listen(ctx, a.dbURL, channel)
	time.Sleep(200 * time.Millisecond) // let LISTEN register
}

// sseEvents opens the stream and returns a channel of decoded events.
func (a *testApp) sseEvents() (<-chan streamEvent, func()) {
	a.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", a.ts.URL+"/notifications/stream", nil)
	resp, err := a.c.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != 200 || ct != "text/event-stream" {
		a.t.Fatalf("stream: %d %s", resp.StatusCode, ct)
	}
	out := make(chan streamEvent, 8)
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var ev streamEvent
				if json.Unmarshal([]byte(data), &ev) == nil {
					out <- ev
				}
			}
		}
		close(out)
	}()
	return out, cancel
}

func next(t *testing.T, events <-chan streamEvent) streamEvent {
	t.Helper()
	select {
	case ev, ok := <-events:
		if !ok {
			t.Fatal("stream closed")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no SSE event within 5s")
	}
	return streamEvent{}
}

func TestReplyNotificationStream(t *testing.T) {
	a := newTestApp(t, nil)
	a.listen()
	a.matureUser("alice", "alice password", 0)
	a.matureUser("bob", "bob password", 0)
	a.login("alice", "alice password")
	thread := a.newThread("Ask me anything", "x")

	events, closeStream := a.sseEvents()
	defer closeStream()
	if ev := next(t, events); ev.Unread != 0 {
		t.Fatalf("initial unread = %d", ev.Unread)
	}

	bob := newClientFor(a)
	bob.login("bob", "bob password")
	bob.post(thread+"/comments", url.Values{"body": {"what's your favourite index?"}}, nil)

	ev := next(t, events)
	if ev.Unread != 1 || ev.Latest == nil || !strings.Contains(ev.Latest.Text, "bob commented on “Ask me anything”") ||
		!strings.HasPrefix(ev.Latest.URL, thread+"#c") {
		t.Fatalf("pushed event: %+v %+v", ev, ev.Latest)
	}

	// Opening the inbox marks everything read and pushes the new count.
	_, page := a.get("/notifications")
	mustContain(t, page, `class="unread"`, "Ask me anything") // rendered before marking read
	if ev := next(t, events); ev.Unread != 0 {
		t.Fatalf("after reading inbox: unread = %d", ev.Unread)
	}
	_, page = a.get("/panorama")
	mustContain(t, page, `<span class="badge" data-unread hidden>0<`)
}

func TestStreamRequiresLoginAndClosesOnShutdown(t *testing.T) {
	a := newTestApp(t, nil)
	if resp, _ := a.get("/notifications/stream"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous stream: %d", resp.StatusCode)
	}
	a.matureUser("alice", "alice password", 0)
	a.login("alice", "alice password")
	events, closeStream := a.sseEvents()
	defer closeStream()
	next(t, events)

	a.srv.CloseStreams()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("unexpected event after shutdown")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream stayed open after CloseStreams")
	}
}
