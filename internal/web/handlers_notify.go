package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/chenbenny31/plurb/internal/store"
)

// maxStreamsPerUser bounds open SSE connections (tabs) per account.
const maxStreamsPerUser = 8

type inboxPage struct {
	Items []store.Notification
}

func (s *Server) inbox(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	items, err := s.store.Notifications(r.Context(), u.ID, 50)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := s.newView(r, "Inbox", inboxPage{items})
	v.Section = "forum"
	s.render(w, r, http.StatusOK, "inbox", v)
	// Rendered with unread highlighting; now they've been seen.
	if err := s.store.MarkNotificationsRead(r.Context(), u.ID); err != nil {
		s.log.Error("mark read", "err", err)
	} else {
		s.hub.Publish(u.ID) // other open tabs drop their badge
	}
}

type streamEvent struct {
	Unread int          `json:"unread"`
	Latest *latestEvent `json:"latest,omitempty"`
}

type latestEvent struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

// notificationStream is a Server-Sent Events endpoint. It sends the unread
// count on connect and again whenever the hub signals this user, plus a
// heartbeat so proxies don't drop the idle connection.
func (s *Server) notificationStream(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if s.hub.Count(u.ID) >= maxStreamsPerUser {
		http.Error(w, "too many open streams", http.StatusTooManyRequests)
		return
	}
	rc := http.NewResponseController(w)
	// The server's WriteTimeout would cut a long-lived stream; lift it here.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		s.log.Warn("sse: can't clear write deadline", "err", err)
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // tell nginx not to buffer
	w.WriteHeader(http.StatusOK)

	signal, cancel := s.hub.Subscribe(u.ID)
	defer cancel()

	send := func() error {
		ev := streamEvent{}
		var err error
		if ev.Unread, err = s.store.UnreadCount(r.Context(), u.ID); err != nil {
			return err
		}
		if ev.Unread > 0 {
			latest, err := s.store.Notifications(r.Context(), u.ID, 1)
			if err != nil {
				return err
			}
			if len(latest) == 1 && !latest[0].Read {
				n := latest[0]
				ev.Latest = &latestEvent{
					Text: notificationText(n),
					URL:  "/forum/t/" + strconv.FormatInt(n.ThreadID, 10) + "#c" + strconv.FormatInt(n.CommentID, 10),
				}
			}
		}
		b, _ := json.Marshal(ev)
		if _, err := fmt.Fprintf(w, "event: unread\ndata: %s\n\n", b); err != nil {
			return err
		}
		return rc.Flush()
	}

	// Tell the browser to wait 5s before reconnecting after a drop.
	fmt.Fprint(w, "retry: 5000\n\n")
	if err := send(); err != nil {
		return
	}
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.streamsClosed:
			return
		case <-signal:
			if err := send(); err != nil {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}

func notificationText(n store.Notification) string {
	if n.Kind == "comment_reply" {
		return n.ActorDisplayName + " replied to your comment in “" + n.ThreadTitle + "”"
	}
	return n.ActorDisplayName + " commented on “" + n.ThreadTitle + "”"
}

// CloseStreams ends all open SSE streams; main registers it with
// http.Server.RegisterOnShutdown so graceful shutdown isn't held up by them.
func (s *Server) CloseStreams() {
	s.closeOnce.Do(func() { close(s.streamsClosed) })
}
