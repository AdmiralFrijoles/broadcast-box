package whip

import "testing"

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
