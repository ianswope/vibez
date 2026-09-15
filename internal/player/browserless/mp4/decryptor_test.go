package mp4

import (
	"bytes"
	"testing"
)

func TestMakeADTSHeader(t *testing.T) {
	cfg := AudioConfig{
		ObjectType: 2, // AAC-LC (profile 1)
		FreqIndex:  4, // 44100 Hz
		ChanConfig: 2, // Stereo
	}
	hdr := MakeADTSHeader(104, cfg)
	if len(hdr) != 7 {
		t.Fatalf("expected 7-byte header, got %d", len(hdr))
	}
	// Check syncword
	if hdr[0] != 0xFF || (hdr[1]&0xF0) != 0xF0 {
		t.Errorf("invalid ADTS syncword: %x %x", hdr[0], hdr[1])
	}
	// Verify length encoding: frame length = 104 + 7 = 111 (0x006F)
	frameLen := (int(hdr[3]&0x03) << 11) | (int(hdr[4]) << 3) | (int(hdr[5]>>5) & 0x07)
	if frameLen != 111 {
		t.Errorf("expected frame length 111, got %d", frameLen)
	}
}

func TestParseAudioConfigDefault(t *testing.T) {
	cfg, err := ParseAudioConfig([]byte("dummy data without esds"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ObjectType != 2 || cfg.FreqIndex != 4 || cfg.ChanConfig != 2 {
		t.Errorf("expected default audio config, got %+v", cfg)
	}
}

func TestParseAudioConfigValid(t *testing.T) {
	// Construct a synthetic esds box with tag 0x05 and payload 0x12 0x10 (AAC-LC, 44100, 2)
	var esds bytes.Buffer
	esds.WriteString("esds")
	esds.Write([]byte{0x00, 0x00, 0x00, 0x00}) // ver/flags
	esds.Write([]byte{0x05, 0x02, 0x12, 0x10}) // tag 0x05, len 2, config 0x12 0x10

	cfg, err := ParseAudioConfig(esds.Bytes())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ObjectType != 2 || cfg.FreqIndex != 4 || cfg.ChanConfig != 2 {
		t.Errorf("expected parsed audio config 2/4/2, got %+v", cfg)
	}
}
