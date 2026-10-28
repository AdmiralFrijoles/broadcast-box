package whip

import (
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/glimesh/broadcast-box/internal/webrtc/codecs"
	"github.com/glimesh/broadcast-box/internal/webrtc/sessions/whep"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"

	pionCodecs "github.com/pion/rtp/codecs"
)

func (w *WHIPSession) audioWriter(remoteTrack *webrtc.TrackRemote, streamKey string) {
	id := remoteTrack.RID()

	if id == "" {
		id = codecs.AudioTrackLabelDefault
	}

	codec := codecs.GetAudioTrackCodec(remoteTrack.Codec().MimeType)
	track, err := w.addAudioTrack(id, streamKey, codec)
	if err != nil {
		slog.Error("AudioWriter.AddTrack.Error", "err", err)
		return
	}

	var steps rtpSteps
	rtpPkt := &rtp.Packet{}
	rtpBuf := make([]byte, 1500)
	for {
		rtpRead, _, err := remoteTrack.Read(rtpBuf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				slog.Info("WHIPSession.AudioWriter.RtpPkt.EndOfStream")
				return
			} else {
				slog.Error("WHIPSession.AudioWriter.RtpPkt.Err", "err", err)
			}
		}

		track.PacketsReceived.Add(1)

		err = rtpPkt.Unmarshal(rtpBuf[:rtpRead])
		if err != nil {
			slog.Error("WHIPSession.AudioWriter.RtpPkt.Error", "err", err)
			continue
		}

		var sessions map[string]*whep.WHEPSession
		if sessionsAny := w.WHEPSessionsSnapshot.Load(); sessionsAny != nil {
			sessions = sessionsAny.(map[string]*whep.WHEPSession)
		}

		timeDiff, sequenceDiff := steps.next(rtpPkt, audioFirstTimeDiff)
		packet := codecs.TrackPacket{
			Layer:        id,
			Packet:       rtpPkt,
			Codec:        codec,
			TimeDiff:     timeDiff,
			SequenceDiff: sequenceDiff,
		}

		for _, whepSession := range sessions {
			whepSession.SendAudioPacket(packet)
		}
	}
}

func (w *WHIPSession) videoWriter(remoteTrack *webrtc.TrackRemote, streamKey string, peerConnection *webrtc.PeerConnection) {
	id := remoteTrack.RID()

	if id == "" {
		id = codecs.VideoTrackLabelDefault
	}

	codec := codecs.GetVideoTrackCodec(remoteTrack.Codec().MimeType)
	track, err := w.addVideoTrack(id, streamKey, codec)
	if err != nil {
		slog.Error("WHIPSession.VideoWriter.AddTrack.Error", "err", err)
		return
	}
	track.Priority = w.getPrioritizedStreamingLayer(id, peerConnection.CurrentRemoteDescription().SDP)
	track.MediaSSRC.Store(uint32(remoteTrack.SSRC()))

	var depacketizer rtp.Depacketizer
	switch codec {
	case codecs.VideoTrackCodecH264:
		depacketizer = &pionCodecs.H264Packet{}
	case codecs.VideoTrackCodecH265:
		depacketizer = &pionCodecs.H265Depacketizer{}
	case codecs.VideoTrackCodecVP8:
		depacketizer = &pionCodecs.VP8Packet{}
	case codecs.VideoTrackCodecVP9:
		depacketizer = &pionCodecs.VP9Packet{}
	case codecs.VideoTrackCodecAV1:
		depacketizer = &pionCodecs.AV1Depacketizer{}
	}

	if depacketizer == nil {
		slog.Error("WHIPSession.VideoWriter.Depacketizer: No depacketizer was found for codec", "codec", codec)
	}

	var steps rtpSteps

	bitrateWindowStart := time.Now()
	bitrateWindowBytes := uint64(0)

	rtpPkt := &rtp.Packet{}
	pktBuf := make([]byte, 1500)
	for {
		rtpRead, _, err := remoteTrack.Read(pktBuf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				slog.Info("WHIPSession.VideoWriter.RtpPkt.EndOfStream")
				w.notifyClosed()
				return
			} else {
				slog.Error("WHIPSession.VideoWriter.RtpPkt.Err", "err", err)
			}
		}

		if rtpRead == 0 {
			continue
		}

		err = rtpPkt.Unmarshal(pktBuf[:rtpRead])
		if err != nil {
			slog.Error("WHIPSession.VideoWriter.RtpPkt.Unmarshal", "err", err)
			continue
		}

		rtpPkt.Extension = false
		rtpPkt.Extensions = nil

		track.PacketsReceived.Add(1)
		bitrateWindowBytes += uint64(rtpRead)

		isKeyframe := isPacketKeyframe(rtpPkt, codec, depacketizer)
		if isKeyframe {
			track.LastKeyFrame.Store(time.Now())
			w.notifyKeyframe()
		}

		now := time.Now()
		if elapsed := now.Sub(bitrateWindowStart); elapsed >= time.Second {
			track.Bitrate.Store(uint64(float64(bitrateWindowBytes) / elapsed.Seconds()))
			bitrateWindowStart = now
			bitrateWindowBytes = 0
		}

		timeDiff, sequenceDiff := steps.next(rtpPkt, videoFirstTimeDiff)

		var sessions map[string]*whep.WHEPSession
		if sessionsAny := w.WHEPSessionsSnapshot.Load(); sessionsAny != nil {
			sessions = sessionsAny.(map[string]*whep.WHEPSession)
		}

		for _, whepSession := range sessions {
			if whepSession.GetVideoLayerOrDefault(id, track.Priority) != id {
				continue
			}

			whepSession.SendVideoPacket(codecs.TrackPacket{
				Layer:        id,
				Packet:       rtpPkt,
				Codec:        codec,
				IsKeyframe:   isKeyframe,
				TimeDiff:     timeDiff,
				SequenceDiff: sequenceDiff,
			})
		}
	}
}

// Viewers get each track's RTP numbering as steps from what they had, so it runs on across publishers.
type rtpSteps struct {
	lastTimestamp uint32
	lastSequence  uint16
	started       bool
}

// A new publisher's first packet steps one sequence number and one frame past the last one viewers
// got. Repeating either makes viewers drop it as a duplicate, and its own numbers make them drop all of it.
func (r *rtpSteps) next(pkt *rtp.Packet, firstTimeDiff int64) (timeDiff int64, sequenceDiff int) {
	timeDiff = int64(pkt.Timestamp) - int64(r.lastTimestamp)
	sequenceDiff = int(pkt.SequenceNumber) - int(r.lastSequence)
	switch {
	case !r.started:
		timeDiff = firstTimeDiff
	case timeDiff < -(math.MaxUint32 / 10):
		timeDiff += math.MaxUint32 + 1
	}
	switch {
	case !r.started:
		sequenceDiff = 1
	case sequenceDiff < -(math.MaxUint16 / 10):
		sequenceDiff += math.MaxUint16 + 1
	}
	r.started = true
	r.lastTimestamp = pkt.Timestamp
	r.lastSequence = pkt.SequenceNumber
	return timeDiff, sequenceDiff
}

const (
	// One frame at 30 fps on the 90 kHz video clock, and one 20 ms Opus frame at 48 kHz.
	videoFirstTimeDiff = 3000
	audioFirstTimeDiff = 960

	naluTypeBitmask = 0x1f

	idrNALUType   = 5
	spsNALUType   = 7
	ppsNALUType   = 8
	stapaNALUType = 24
	fuaNALUType   = 28

	fuStartBitmask = 0x80
)

func isPacketKeyframe(pkt *rtp.Packet, codec codecs.TrackCodeType, _ rtp.Depacketizer) bool {
	if codec == codecs.VideoTrackCodecH264 {
		return isH264KeyframeStart(pkt.Payload)
	}

	return true
}

// Reports whether the packet starts a keyframe. The depacketizer returns a fragmented NAL only on
// its last packet, and a viewer started there misses the head of the frame.
func isH264KeyframeStart(payload []byte) bool {
	if len(payload) < 2 {
		return false
	}

	switch naluType := payload[0] & naluTypeBitmask; naluType {
	case stapaNALUType:
		for offset := 1; offset+2 <= len(payload); {
			size := int(binary.BigEndian.Uint16(payload[offset:]))
			offset += 2
			if size == 0 || offset+size > len(payload) {
				return false
			}
			if isH264KeyframeNALU(payload[offset] & naluTypeBitmask) {
				return true
			}
			offset += size
		}
		return false
	case fuaNALUType:
		return payload[1]&fuStartBitmask != 0 && payload[1]&naluTypeBitmask == idrNALUType
	default:
		return isH264KeyframeNALU(naluType)
	}
}

func isH264KeyframeNALU(naluType byte) bool {
	return naluType == idrNALUType || naluType == spsNALUType || naluType == ppsNALUType
}

// Helper function for getting the simulcast order and using as priority for consumers
// This example will order from left to right with highest to lowest priority
// a=simulcast:send High,Mid,Low
func (w *WHIPSession) getPrioritizedStreamingLayer(layer string, sdpDescription string) int {
	var sessionDescription sdp.SessionDescription
	err := sessionDescription.Unmarshal([]byte(sdpDescription))
	if err != nil {
		slog.Error("Track.getPrioritizedStreamingLayer Error", "layer", layer, "err", err)
		return 100
	}

	var priority = 1
	for _, description := range sessionDescription.MediaDescriptions {
		for _, attribute := range description.Attributes {
			if attribute.Key == "simulcast" && strings.HasPrefix(attribute.Value, "send ") {
				layers := strings.TrimPrefix(attribute.Value, "send")
				slog.Info("WHIPSession.VideoWriter.TrackPriority", "layers", layers)
				for simulcastLayer := range strings.SplitSeq(strings.TrimSpace(layers), ";") {
					if simulcastLayer != "" && strings.EqualFold(simulcastLayer, layer) {
						slog.Info("WHIPSession.VideoWriter.TrackPriority", "layer", layer)
						return priority
					} else {
						priority++
					}
				}
			}
		}
	}

	return 100
}
