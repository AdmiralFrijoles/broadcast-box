package session

import "testing"

func TestMediaEventsReachSubscribersAndReplay(t *testing.T) {
	s := &Session{}

	early, replay, cancel := s.SubscribeMedia()
	defer cancel()
	if len(replay) != 0 {
		t.Fatalf("expected no replay before any events, got %d", len(replay))
	}

	s.PublishMediaEvent("tracks", `{"type":"tracks"}`)
	s.PublishMediaEvent("cue", `{"type":"cue"}`)
	s.PublishMediaEvent("playback", `{"type":"playback"}`)

	for _, want := range []string{`{"type":"tracks"}`, `{"type":"cue"}`, `{"type":"playback"}`} {
		if got := <-early; got != mediaEvent(want) {
			t.Fatalf("got %q, want %q", got, mediaEvent(want))
		}
	}

	_, replay, cancelLate := s.SubscribeMedia()
	defer cancelLate()
	if len(replay) != 2 || replay[0] != mediaEvent(`{"type":"tracks"}`) || replay[1] != mediaEvent(`{"type":"playback"}`) {
		t.Fatalf("late subscriber replay was %q", replay)
	}

	s.endMedia()
	if got := <-early; got != mediaEvent(`{"type":"end"}`) {
		t.Fatalf("expected end event, got %q", got)
	}
	_, replay, cancelAfter := s.SubscribeMedia()
	defer cancelAfter()
	if len(replay) != 0 {
		t.Fatalf("expected no replay after end, got %q", replay)
	}
}

func TestTitleReplaysFirstAndClearsAtEnd(t *testing.T) {
	s := &Session{}
	s.PublishMediaEvent("tracks", `{"type":"tracks"}`)
	s.PublishMediaEvent("title", `{"type":"title","title":"Demo"}`)

	_, replay, cancel := s.SubscribeMedia()
	defer cancel()
	if len(replay) != 2 || replay[0] != mediaEvent(`{"type":"title","title":"Demo"}`) {
		t.Fatalf("title should replay first, got %q", replay)
	}

	s.endMedia()
	_, replay, cancelAfter := s.SubscribeMedia()
	defer cancelAfter()
	if len(replay) != 0 {
		t.Fatalf("expected no replay after end, got %q", replay)
	}
}

func TestSlowMediaSubscriberDoesNotBlock(t *testing.T) {
	s := &Session{}
	_, _, cancel := s.SubscribeMedia()
	defer cancel()
	for i := 0; i < mediaSubscriberBuffer*3; i++ {
		s.PublishMediaEvent("cue", `{"type":"cue"}`)
	}
}
