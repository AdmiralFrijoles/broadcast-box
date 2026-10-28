package webrtc

import (
	"errors"
	"log/slog"

	"github.com/glimesh/broadcast-box/internal/server/authorization"
	"github.com/glimesh/broadcast-box/internal/webrtc/peerconnection"
	"github.com/glimesh/broadcast-box/internal/webrtc/sessions/manager"
	"github.com/glimesh/broadcast-box/internal/webrtc/utils"
)

// Initialize WHIP session for incoming stream
func WHIP(offer string, profile authorization.PublicProfile) (sdp string, sessionID string, err error) {
	slog.Info("WHIP.Offer.Requested", "streamKey", profile.StreamKey, "motd", profile.MOTD)

	if err := utils.ValidateOffer(offer); err != nil {
		return "", "", errors.New("invalid offer: " + err.Error())
	}

	session, err := manager.SessionsManager.GetOrAddSession(profile, true)
	if err != nil {
		return "", "", err
	}

	peerConnection, err := peerconnection.CreateWHIPPeerConnection(offer)
	if err != nil || peerConnection == nil {
		slog.Error("WHIP.CreateWHIPPeerConnection.Failed", "err", err)
		if peerConnection != nil {
			if closeErr := peerConnection.Close(); closeErr != nil {
				slog.Error("WHIP.CreateWHIPPeerConnection.Close.Failed", "err", closeErr)
			}
		}
		return "", "", err
	}

	host, err := session.AddHost(peerConnection)
	if err != nil {
		return "", "", err
	}

	sdp = utils.DebugOutputAnswer(utils.AppendCandidateToAnswer(peerConnection.LocalDescription().SDP))
	sessionID = host.ID
	err = nil
	slog.Info("WHIP.Offer.Accepted", "streamKey", profile.StreamKey, "motd", profile.MOTD)
	return
}
