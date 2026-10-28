package whip

import (
	"testing"

	"github.com/pion/rtp"
)

func TestH264KeyframeStart(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    bool
	}{
		{"single SPS", []byte{0x67, 0x42}, true},
		{"single IDR", []byte{0x65, 0x88}, true},
		{"single P slice", []byte{0x41, 0x9a}, false},
		{"STAP-A with SEI then SPS", []byte{0x78, 0x00, 0x02, 0x06, 0x05, 0x00, 0x02, 0x67, 0x42}, true},
		{"STAP-A with SEI only", []byte{0x78, 0x00, 0x02, 0x06, 0x05}, false},
		{"FU-A IDR start", []byte{0x7c, 0x85, 0x88}, true},
		{"FU-A IDR end", []byte{0x7c, 0x45, 0x88}, false},
		{"FU-A P slice start", []byte{0x5c, 0x81, 0x9a}, false},
		{"short", []byte{0x65}, false},
	}
	for _, c := range cases {
		if got := isH264KeyframeStart(c.payload); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRTPStepsRunOnAcrossPublishers(t *testing.T) {
	var steps rtpSteps
	timeDiff, sequenceDiff := steps.next(&rtp.Packet{Header: rtp.Header{SequenceNumber: 40000, Timestamp: 7000}}, 960)
	if timeDiff != 960 || sequenceDiff != 1 {
		t.Fatalf("first packet stepped %d, %d", timeDiff, sequenceDiff)
	}
	timeDiff, sequenceDiff = steps.next(&rtp.Packet{Header: rtp.Header{SequenceNumber: 40001, Timestamp: 7960}}, 960)
	if timeDiff != 960 || sequenceDiff != 1 {
		t.Fatalf("next packet stepped %d, %d", timeDiff, sequenceDiff)
	}
	timeDiff, sequenceDiff = steps.next(&rtp.Packet{Header: rtp.Header{SequenceNumber: 2, Timestamp: 100}}, 960)
	if sequenceDiff != 65536-40001+2 || timeDiff != 100-7960 {
		t.Fatalf("a wrapped sequence stepped %d, %d", timeDiff, sequenceDiff)
	}
}
