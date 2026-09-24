package whip

import (
	"encoding/json"
	"io"
	"net/http"
	"os"

	"github.com/glimesh/broadcast-box/internal/environment"
	"github.com/glimesh/broadcast-box/internal/server/authorization"
	"github.com/glimesh/broadcast-box/internal/server/helpers"
	"github.com/glimesh/broadcast-box/internal/webrtc/sessions/manager"
)

const maxMediaEventBytes = 64 * 1024

var mediaEventTypes = map[string]bool{
	"title": true, "tracks": true, "playback": true, "cue": true, "seek": true, "end": true,
}

// MediaHandler takes playback and subtitle events from the stream's host and relays them to its viewers.
func MediaHandler(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		helpers.LogHTTPError(responseWriter, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := helpers.ResolveBearerToken(request.Header.Get("Authorization"))
	streamKey := mediaStreamKey(token)
	if streamKey == "" {
		responseWriter.WriteHeader(http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(request.Body, maxMediaEventBytes+1))
	if err != nil || len(body) > maxMediaEventBytes {
		helpers.LogHTTPError(responseWriter, "Invalid media event", http.StatusBadRequest)
		return
	}

	var event map[string]any
	if err := json.Unmarshal(body, &event); err != nil {
		helpers.LogHTTPError(responseWriter, "Invalid media event", http.StatusBadRequest)
		return
	}
	kind, _ := event["type"].(string)
	if !mediaEventTypes[kind] {
		helpers.LogHTTPError(responseWriter, "Invalid media event", http.StatusBadRequest)
		return
	}

	// Marshalling again keeps the event on one line, which an SSE data field needs.
	data, err := json.Marshal(event)
	if err != nil {
		helpers.LogHTTPError(responseWriter, "Invalid media event", http.StatusBadRequest)
		return
	}

	streamSession, found := manager.SessionsManager.GetSessionByID(streamKey)
	if !found || !streamSession.HasHost.Load() {
		helpers.LogHTTPError(responseWriter, "No active stream found", http.StatusNotFound)
		return
	}

	streamSession.PublishMediaEvent(kind, string(data))
	responseWriter.WriteHeader(http.StatusNoContent)
}

// Resolves the token the same way WHIPHandler does. Returns "" when it may not publish.
func mediaStreamKey(token string) string {
	if token == "" {
		return ""
	}
	if profile, _ := authorization.GetPublicProfile(token); profile != nil {
		return profile.StreamKey
	}
	if os.Getenv(environment.StreamProfilePolicy) == authorization.StreamPolicyReservedOnly {
		return ""
	}
	if authorization.IsProfileReserved(token) {
		return ""
	}
	return token
}
