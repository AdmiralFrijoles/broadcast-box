package session

import (
	"testing"

	"github.com/glimesh/broadcast-box/internal/webrtc/sessions/whep"
	"github.com/glimesh/broadcast-box/internal/webrtc/sessions/whip"
)

func newTestHost(id string) *whip.WHIPSession {
	host := &whip.WHIPSession{
		ID:          id,
		AudioTracks: make(map[string]*whip.AudioTrack),
		VideoTracks: make(map[string]*whip.VideoTrack),
	}
	host.WHEPSessionsSnapshot.Store(make(map[string]*whep.WHEPSession))
	return host
}

func newTestSession(current, pending *whip.WHIPSession) *Session {
	s := &Session{WHEPSessions: make(map[string]*whep.WHEPSession)}
	if current != nil {
		s.Host.Store(current)
		s.HasHost.Store(true)
	}
	if pending != nil {
		s.PendingHost.Store(pending)
	}
	return s
}

func TestPendingHostTakesOverOnKeyframe(t *testing.T) {
	old, next := newTestHost("old"), newTestHost("next")
	s := newTestSession(old, next)
	s.PublishMediaEvent("title", `{"type":"title"}`)
	events, _, cancelEvents := s.SubscribeMedia()
	defer cancelEvents()

	s.promoteHost(next)
	if got := <-events; got != mediaEvent(`{"type":"switch"}`) {
		t.Fatalf("viewers were not told about the switch, got %q", got)
	}

	if s.Host.Load() != next || s.PendingHost.Load() != nil || !s.HasHost.Load() {
		t.Fatalf("pending host did not take over")
	}
	_, replay, cancel := s.SubscribeMedia()
	defer cancel()
	if len(replay) != 1 {
		t.Fatalf("takeover should keep the title, replay was %q", replay)
	}

	s.handleHostClosed(old)
	if s.Host.Load() != next || !s.HasHost.Load() {
		t.Fatalf("the replaced host closing removed its successor")
	}
}

func TestPromotingANonPendingHostDoesNothing(t *testing.T) {
	old, stray := newTestHost("old"), newTestHost("stray")
	s := newTestSession(old, nil)

	s.promoteHost(stray)

	if s.Host.Load() != old {
		t.Fatalf("a host that was never pending took over")
	}
}

func TestPendingHostTakesOverWhenCurrentLeaves(t *testing.T) {
	old, next := newTestHost("old"), newTestHost("next")
	s := newTestSession(old, next)

	events, _, cancelEvents := s.SubscribeMedia()
	defer cancelEvents()

	if s.RemoveHostByID("old") {
		t.Fatalf("stream reported as ended although a host was pending")
	}
	if got := <-events; got != mediaEvent(`{"type":"switch"}`) {
		t.Fatalf("viewers were not told about the switch, got %q", got)
	}
	if s.Host.Load() != next || s.PendingHost.Load() != nil || !s.HasHost.Load() {
		t.Fatalf("pending host did not take over")
	}
}

func TestRemovingPendingHostKeepsCurrent(t *testing.T) {
	old, next := newTestHost("old"), newTestHost("next")
	s := newTestSession(old, next)

	if s.RemoveHostByID("next") {
		t.Fatalf("stream reported as ended when only the pending host left")
	}
	if s.Host.Load() != old || s.PendingHost.Load() != nil {
		t.Fatalf("removing the pending host changed the current one")
	}
}

func TestRemovingLastHostEndsStream(t *testing.T) {
	old := newTestHost("old")
	s := newTestSession(old, nil)
	s.PublishMediaEvent("title", `{"type":"title"}`)

	if !s.RemoveHostByID("old") {
		t.Fatalf("stream not reported as ended")
	}
	if s.Host.Load() != nil || s.HasHost.Load() {
		t.Fatalf("host still set after removal")
	}
	_, replay, cancel := s.SubscribeMedia()
	defer cancel()
	if len(replay) != 0 {
		t.Fatalf("media should end with the stream, replay was %q", replay)
	}
}
