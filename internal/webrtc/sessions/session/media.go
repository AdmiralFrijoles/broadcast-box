package session

import "sync"

// Media events come from the host: title, and while it plays a file tracks, playback, cue, seek and end.
// Title, tracks and playback are kept so viewers who join later can catch up.
type mediaState struct {
	lock        sync.Mutex
	title       string
	tracks      string
	playback    string
	subscribers map[chan string]struct{}
}

const mediaSubscriberBuffer = 64

func mediaEvent(data string) string {
	return "event: media\ndata: " + data + "\n\n"
}

// PublishMediaEvent relays one host event to every viewer. data must be JSON on a single line.
func (s *Session) PublishMediaEvent(kind string, data string) {
	s.media.lock.Lock()
	defer s.media.lock.Unlock()

	switch kind {
	case "title":
		s.media.title = data
	case "tracks":
		s.media.tracks = data
	case "playback":
		s.media.playback = data
	case "end":
		s.media.title = ""
		s.media.tracks = ""
		s.media.playback = ""
	}

	event := mediaEvent(data)
	for subscriber := range s.media.subscribers {
		// A viewer that has stopped reading misses events rather than blocking the host.
		select {
		case subscriber <- event:
		default:
		}
	}
}

// SubscribeMedia returns a channel of media events, the events to replay first, and a cancel function.
func (s *Session) SubscribeMedia() (chan string, []string, func()) {
	subscriber := make(chan string, mediaSubscriberBuffer)

	s.media.lock.Lock()
	if s.media.subscribers == nil {
		s.media.subscribers = make(map[chan string]struct{})
	}
	s.media.subscribers[subscriber] = struct{}{}
	replay := []string{}
	for _, data := range []string{s.media.title, s.media.tracks, s.media.playback} {
		if data != "" {
			replay = append(replay, mediaEvent(data))
		}
	}
	s.media.lock.Unlock()

	cancel := func() {
		s.media.lock.Lock()
		delete(s.media.subscribers, subscriber)
		s.media.lock.Unlock()
	}
	return subscriber, replay, cancel
}

func (s *Session) endMedia() {
	s.media.lock.Lock()
	hadMedia := s.media.title != "" || s.media.tracks != "" || s.media.playback != ""
	s.media.lock.Unlock()

	if hadMedia {
		s.PublishMediaEvent("end", `{"type":"end"}`)
	}
}
