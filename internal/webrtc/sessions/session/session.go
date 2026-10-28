package session

import (
	"log/slog"

	"github.com/glimesh/broadcast-box/internal/server/authorization"
	"github.com/glimesh/broadcast-box/internal/webrtc/codecs"
	"github.com/glimesh/broadcast-box/internal/webrtc/sessions/whep"
	"github.com/glimesh/broadcast-box/internal/webrtc/sessions/whip"
	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"
)

func (s *Session) UpdateStreamStatus(profile authorization.PublicProfile) {
	s.StatusLock.Lock()

	s.MOTD = profile.MOTD
	s.IsPublic = profile.IsPublic

	s.StatusLock.Unlock()
}

func (session *Session) SetOnClose(onClose func()) {
	session.onClose = onClose
}

// Add WHEP viewer session
func (s *Session) AddWHEP(whepSessionID string, peerConnection *webrtc.PeerConnection, audioTrack *codecs.TrackMultiCodec, videoTrack *codecs.TrackMultiCodec, videoRTCPSender *webrtc.RTPSender, pliSender func()) (err error) {
	slog.Debug("WHIPSessionManager.WHIPSession.AddWHEPSession")

	whepSession := whep.CreateNewWHEP(
		whepSessionID,
		s.StreamKey,
		audioTrack,
		videoTrack,
		peerConnection,
		pliSender,
	)

	whepSession.SetOnClose(s.handleWHEPClose)

	s.WHEPSessionsLock.Lock()
	s.WHEPSessions[whepSessionID] = whepSession
	s.WHEPSessionsLock.Unlock()
	s.updateHostWHEPSessionsSnapshot()
	whepSession.RegisterWHEPHandlers(peerConnection)
	s.registerDataChannelHandlers(peerConnection, whepSessionID)
	go s.handleWHEPVideoRTCPSender(whepSession, videoRTCPSender)

	return nil
}

// Add host. A host that arrives while another streams waits as pending and takes over on its first keyframe.
func (s *Session) AddHost(peerConnection *webrtc.PeerConnection) (*whip.WHIPSession, error) {
	slog.Debug("Session.AddHost")

	host := &whip.WHIPSession{
		ID:          uuid.New().String(),
		AudioTracks: make(map[string]*whip.AudioTrack),
		VideoTracks: make(map[string]*whip.VideoTrack),
	}
	host.WHEPSessionsSnapshot.Store(make(map[string]*whep.WHEPSession))
	host.SetOnClosed(func() { s.handleHostClosed(host) })
	host.SetOnKeyframe(func() { s.promoteHost(host) })

	host.AddPeerConnection(peerConnection, s.StreamKey)
	s.registerDataChannelHandlers(peerConnection, host.ID)

	var retired []*whip.WHIPSession
	s.hostLock.Lock()
	if current := s.Host.Load(); current != nil && current.IsOpen() {
		slog.Info("Session.AddHost: Pending until keyframe", "streamKey", s.StreamKey, "id", host.ID)
		if previous := s.PendingHost.Swap(host); previous != nil {
			retired = append(retired, previous)
		}
	} else {
		retired = s.installHostLocked(host)
	}
	s.hostLock.Unlock()
	retireHosts(retired)

	return host, nil
}

// Call with hostLock held. Returns the replaced host, for retireHosts once the lock is released.
func (s *Session) installHostLocked(host *whip.WHIPSession) (retired []*whip.WHIPSession) {
	if previous := s.Host.Swap(host); previous != nil {
		previous.WHEPSessionsSnapshot.Store(make(map[string]*whep.WHEPSession))
		retired = append(retired, previous)
	}
	s.resetWHEPSessionsForNewHost()
	s.updateHostWHEPSessionsSnapshot()
	s.HasHost.Store(true)
	return retired
}

// Runs on the pending host's video goroutine, before its keyframe is forwarded.
func (s *Session) promoteHost(host *whip.WHIPSession) {
	s.hostLock.Lock()
	if s.PendingHost.Load() != host {
		s.hostLock.Unlock()
		return
	}
	s.PendingHost.Store(nil)
	slog.Info("Session.PromoteHost", "streamKey", s.StreamKey, "id", host.ID)
	retired := s.installHostLocked(host)
	s.hostLock.Unlock()
	s.publishSwitch()

	// Closing the old peer connection here would hold up the keyframe.
	go retireHosts(retired)
}

// Tells viewers the picture is about to change source, so they can blend into it.
func (s *Session) publishSwitch() {
	s.PublishMediaEvent("switch", `{"type":"switch"}`)
}

func retireHosts(hosts []*whip.WHIPSession) {
	for _, host := range hosts {
		host.RemovePeerConnection()
		host.RemoveTracks()
	}
}

// Removes the current host and any pending one.
func (s *Session) RemoveHost() {
	if pending := s.PendingHost.Load(); pending != nil {
		s.removeHost(pending)
	}
	host := s.Host.Load()
	if host == nil {
		slog.Info("Session.RemoveHost", "streamKey", s.StreamKey, "msg", "No host to remove")
		return
	}
	s.removeHost(host)
}

// Returns true when the stream lost its host and nothing took its place.
func (s *Session) RemoveHostByID(id string) bool {
	for _, host := range []*whip.WHIPSession{s.PendingHost.Load(), s.Host.Load()} {
		if host != nil && host.ID == id {
			return s.removeHost(host)
		}
	}
	return false
}

// Returns true when the stream lost its host and nothing took its place.
// A pending host takes over at once when the current one leaves.
func (s *Session) removeHost(host *whip.WHIPSession) bool {
	if host == nil {
		return false
	}

	var retired []*whip.WHIPSession
	ended, switched := false, false
	s.hostLock.Lock()
	switch host {
	case s.PendingHost.Load():
		s.PendingHost.Store(nil)
		retired = append(retired, host)
	case s.Host.Load():
		slog.Info("Session.RemoveHost", "streamKey", s.StreamKey, "id", host.ID)
		if pending := s.PendingHost.Swap(nil); pending != nil {
			retired = s.installHostLocked(pending)
			switched = true
		} else {
			s.Host.Store(nil)
			s.HasHost.Store(false)
			host.WHEPSessionsSnapshot.Store(make(map[string]*whep.WHEPSession))
			retired = append(retired, host)
			ended = true
		}
	}
	s.hostLock.Unlock()

	retireHosts(retired)
	if ended {
		s.endMedia()
	} else if switched {
		s.publishSwitch()
	}
	return ended
}

func (s *Session) handleWHEPClose(whepSessionID string) {
	slog.Info("Session.HandleWHEPClose", "streamKey", s.StreamKey, "whepSessionID", whepSessionID)

	s.WHEPSessionsLock.Lock()
	_, ok := s.WHEPSessions[whepSessionID]
	if ok {
		delete(s.WHEPSessions, whepSessionID)
	}
	s.WHEPSessionsLock.Unlock()

	if !ok {
		return
	}

	s.updateHostWHEPSessionsSnapshot()

	if s.isEmpty() {
		s.close()
	}
}

func (s *Session) handleHostClosed(host *whip.WHIPSession) {
	if s.removeHost(host) && s.isEmpty() {
		s.close()
	}
}

// Remove all Hosts and clients before closing down session
func (s *Session) close() {
	s.closeOnce.Do(func() {
		s.WHEPSessionsLock.Lock()
		whepSessions := make([]*whep.WHEPSession, 0, len(s.WHEPSessions))
		for _, whepSession := range s.WHEPSessions {
			whepSessions = append(whepSessions, whepSession)
		}
		s.WHEPSessions = make(map[string]*whep.WHEPSession)
		s.WHEPSessionsLock.Unlock()

		for _, whepSession := range whepSessions {
			whepSession.Close()
		}
		s.updateHostWHEPSessionsSnapshot()

		s.RemoveHost()

		if s.onClose != nil {
			s.onClose()
		}
	})
}

func (s *Session) Close() {
	slog.Info("Session.Close", "streamKey", s.StreamKey)
	s.close()
}

// Returns true is no WHIP tracks are present, and no WHEP sessions are waiting for incoming streams
func (s *Session) isEmpty() bool {
	if s.hasWHEPSessions() {
		slog.Debug("Session.IsEmpty.HasWHEPSessions (false)", "streamKey", s.StreamKey)
		return false
	}

	if s.isStreaming() {
		slog.Debug("Session.IsEmpty.IsActive (false)", "streamKey", s.StreamKey)
		return false
	}

	slog.Debug("Session.IsEmpty (true)", "streamKey", s.StreamKey)
	return true
}

// Returns true if any tracks are available for the session
func (s *Session) isStreaming() bool {

	host := s.Host.Load()
	if host == nil {
		return false
	}

	host.TracksLock.RLock()

	if len(host.AudioTracks) != 0 {
		slog.Debug("Session.IsActive.AudioTracks", "count", len(host.AudioTracks))
		host.TracksLock.RUnlock()
		return true
	}
	if len(host.VideoTracks) != 0 {
		slog.Debug("Session.IsActive.VideoTracks", "count", len(host.VideoTracks))
		host.TracksLock.RUnlock()
		return true
	}

	host.TracksLock.RUnlock()
	return false
}

func (s *Session) hasWHEPSessions() bool {
	s.WHEPSessionsLock.RLock()
	slog.Debug("Session.HasWHEPSessions", "count", len(s.WHEPSessions))

	if len(s.WHEPSessions) == 0 {
		s.WHEPSessionsLock.RUnlock()
		return false
	}

	s.WHEPSessionsLock.RUnlock()
	return true
}

func (s *Session) updateHostWHEPSessionsSnapshot() {
	host := s.Host.Load()
	if host == nil {
		return
	}

	s.WHEPSessionsLock.RLock()
	snapshot := make(map[string]*whep.WHEPSession, len(s.WHEPSessions))
	for _, whepSession := range s.WHEPSessions {
		if !whepSession.IsSessionClosed.Load() {
			snapshot[whepSession.SessionID] = whepSession
		}
	}
	s.WHEPSessionsLock.RUnlock()

	host.WHEPSessionsSnapshot.Store(snapshot)
}

func (s *Session) resetWHEPSessionsForNewHost() {
	s.WHEPSessionsLock.RLock()
	for _, whepSession := range s.WHEPSessions {
		if whepSession == nil {
			continue
		}

		whepSession.ResetForNewPublisher()
	}
	s.WHEPSessionsLock.RUnlock()
}

// Get the status of the current session
func (s *Session) GetStreamStatus() (status whipSessionStatus) {
	s.WHEPSessionsLock.RLock()
	whepSessionsCount := len(s.WHEPSessions)
	s.WHEPSessionsLock.RUnlock()

	s.StatusLock.RLock()

	status = whipSessionStatus{
		StreamKey:   s.StreamKey,
		MOTD:        s.MOTD,
		ViewerCount: whepSessionsCount,
		IsOnline:    s.HasHost.Load(),
		StreamStart: s.StreamStart,
	}

	s.StatusLock.RUnlock()

	return
}
