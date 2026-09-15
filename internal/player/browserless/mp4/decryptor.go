package mp4

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// AudioConfig stores the audio metadata extracted from the init segment (esds box).
type AudioConfig struct {
	ObjectType int // 2 = AAC LC (profile = 1 in ADTS)
	FreqIndex  int // 4 = 44100 Hz, 3 = 48000 Hz, etc.
	ChanConfig int // 2 = Stereo, 1 = Mono
}

// DefaultAudioConfig returns the standard Apple Music AAC-LC 44.1kHz Stereo config.
func DefaultAudioConfig() AudioConfig {
	return AudioConfig{
		ObjectType: 2,
		FreqIndex:  4,
		ChanConfig: 2,
	}
}

// ParseAudioConfig parses the AudioSpecificConfig from the init segment (ftyp + moov).
func ParseAudioConfig(initData []byte) (AudioConfig, error) {
	cfg := DefaultAudioConfig()
	esdsIdx := bytes.Index(initData, []byte("esds"))
	if esdsIdx == -1 {
		return cfg, nil // Fallback to default
	}

	// Scan for tag 0x05 (DecSpecificInfoTag) within esds box
	boxEnd := len(initData)
	if esdsIdx+128 < boxEnd {
		boxEnd = esdsIdx + 128
	}
	esdsData := initData[esdsIdx:boxEnd]
	tag5Idx := bytes.Index(esdsData, []byte{0x05})
	if tag5Idx != -1 && tag5Idx+5 < len(esdsData) {
		// Variable length descriptor: length could be 0x80 0x80 0x80 0x02 or 0x02
		offset := tag5Idx + 1
		for offset < len(esdsData) && (esdsData[offset]&0x80) != 0 {
			offset++
		}
		offset++ // skip length byte
		if offset+1 < len(esdsData) {
			b0 := esdsData[offset]
			b1 := esdsData[offset+1]
			cfg.ObjectType = int(b0 >> 3)
			cfg.FreqIndex = int(((b0 & 0x07) << 1) | (b1 >> 7))
			cfg.ChanConfig = int((b1 >> 3) & 0x0F)
		}
	}

	return cfg, nil
}

// MakeADTSHeader generates a 7-byte ADTS header for an AAC frame.
func MakeADTSHeader(sampleLen int, cfg AudioConfig) []byte {
	profile := cfg.ObjectType - 1
	if profile < 0 {
		profile = 1
	}
	freqIdx := cfg.FreqIndex
	chanCfg := cfg.ChanConfig
	frameLen := sampleLen + 7

	header := make([]byte, 7)
	header[0] = 0xFF
	header[1] = 0xF1 // MPEG-4, Layer 0, No protection (1)
	header[2] = byte(((profile & 0x03) << 6) | ((freqIdx & 0x0F) << 2) | ((chanCfg >> 2) & 0x01))
	header[3] = byte(((chanCfg & 0x03) << 6) | ((frameLen >> 11) & 0x03))
	header[4] = byte((frameLen >> 3) & 0xFF)
	header[5] = byte(((frameLen & 0x07) << 5) | 0x1F)
	header[6] = 0xFC
	return header
}

// Subsample represents clear and encrypted byte ranges within a sample.
type Subsample struct {
	ClearBytes  uint16
	CipherBytes uint32
}

// SampleEncInfo contains the IV and subsamples for one audio frame.
type SampleEncInfo struct {
	IV         []byte
	Subsamples []Subsample
}

// DecryptSegment parses a fragmented MP4 audio segment (moof + mdat) and uses
// the provided decrypt function to decrypt samples, returning an elementary AAC bitstream.
func DecryptSegment(
	segData []byte,
	kid []byte,
	cfg AudioConfig,
	decryptFn func(kid, iv, ciphertext []byte) ([]byte, error),
) ([]byte, error) {
	// Find moof box
	moofIdx := bytes.Index(segData, []byte("moof"))
	if moofIdx < 4 {
		return nil, fmt.Errorf("moof box not found in segment")
	}
	moofStart := moofIdx - 4
	moofSize := int(binary.BigEndian.Uint32(segData[moofStart : moofStart+4]))
	if moofStart+moofSize > len(segData) {
		return nil, fmt.Errorf("invalid moof size: %d", moofSize)
	}
	moofData := segData[moofStart : moofStart+moofSize]

	// 1. Parse senc box
	sencIdx := bytes.Index(moofData, []byte("senc"))
	if sencIdx < 4 {
		return nil, fmt.Errorf("senc box not found in moof")
	}
	sencStart := sencIdx - 4
	flags := binary.BigEndian.Uint32([]byte{0, moofData[sencStart+9], moofData[sencStart+10], moofData[sencStart+11]})
	hasSubsamples := (flags & 0x02) != 0
	sampleCount := binary.BigEndian.Uint32(moofData[sencStart+12 : sencStart+16])

	samplesEnc := make([]SampleEncInfo, sampleCount)
	offset := sencStart + 16
	ivSize := 8 // Default for Apple Music CENC

	for i := uint32(0); i < sampleCount; i++ {
		if offset+ivSize > len(moofData) {
			return nil, fmt.Errorf("unexpected EOF in senc IVs at sample %d", i)
		}
		iv := make([]byte, ivSize)
		copy(iv, moofData[offset:offset+ivSize])
		offset += ivSize

		var subsamples []Subsample
		if hasSubsamples {
			if offset+2 > len(moofData) {
				return nil, fmt.Errorf("unexpected EOF in senc subsamples at sample %d", i)
			}
			subCount := binary.BigEndian.Uint16(moofData[offset : offset+2])
			offset += 2
			subsamples = make([]Subsample, subCount)
			for s := uint16(0); s < subCount; s++ {
				if offset+6 > len(moofData) {
					return nil, fmt.Errorf("unexpected EOF in senc subsample entry")
				}
				clearBytes := binary.BigEndian.Uint16(moofData[offset : offset+2])
				cipherBytes := binary.BigEndian.Uint32(moofData[offset+2 : offset+6])
				offset += 6
				subsamples[s] = Subsample{
					ClearBytes:  clearBytes,
					CipherBytes: cipherBytes,
				}
			}
		}
		samplesEnc[i] = SampleEncInfo{
			IV:         iv,
			Subsamples: subsamples,
		}
	}

	// 2. Parse trun box
	trunIdx := bytes.Index(moofData, []byte("trun"))
	if trunIdx < 4 {
		return nil, fmt.Errorf("trun box not found in moof")
	}
	trunStart := trunIdx - 4
	trunFlags := binary.BigEndian.Uint32([]byte{0, moofData[trunStart+9], moofData[trunStart+10], moofData[trunStart+11]})
	trunOffset := trunStart + 16

	dataOffset := moofSize + 8 // Default fallback to right after moof + mdat header
	if (trunFlags & 0x01) != 0 {
		dataOffset = int(int32(binary.BigEndian.Uint32(moofData[trunOffset : trunOffset+4])))
		trunOffset += 4
	}
	if (trunFlags & 0x04) != 0 {
		trunOffset += 4 // first_sample_flags
	}

	hasDuration := (trunFlags & 0x100) != 0
	hasSize := (trunFlags & 0x200) != 0
	hasFlags := (trunFlags & 0x400) != 0
	hasCompTime := (trunFlags & 0x800) != 0

	sampleSizes := make([]int, sampleCount)
	for i := uint32(0); i < sampleCount; i++ {
		if hasDuration {
			trunOffset += 4
		}
		if hasSize {
			sampleSizes[i] = int(binary.BigEndian.Uint32(moofData[trunOffset : trunOffset+4]))
			trunOffset += 4
		}
		if hasFlags {
			trunOffset += 4
		}
		if hasCompTime {
			trunOffset += 4
		}
	}

	// 3. Locate mdat and decrypt samples
	if moofStart+dataOffset > len(segData) {
		return nil, fmt.Errorf("invalid dataOffset: %d", dataOffset)
	}
	mdatData := segData[moofStart+dataOffset:]
	var outBuf bytes.Buffer
	currMdatOffset := 0

	for i := 0; i < int(sampleCount); i++ {
		sSize := sampleSizes[i]
		if currMdatOffset+sSize > len(mdatData) {
			return nil, fmt.Errorf("sample %d exceeds mdat data bounds", i)
		}
		sampleBytes := mdatData[currMdatOffset : currMdatOffset+sSize]
		currMdatOffset += sSize

		encInfo := samplesEnc[i]

		var decryptedSample []byte
		if len(encInfo.Subsamples) == 0 || (len(encInfo.Subsamples) == 1 && encInfo.Subsamples[0].ClearBytes == 0) {
			// Fully encrypted sample
			var err error
			decryptedSample, err = decryptFn(kid, encInfo.IV, sampleBytes)
			if err != nil {
				return nil, fmt.Errorf("failed to decrypt sample %d: %w", i, err)
			}
		} else {
			// Subsample decryption
			var decryptedBuf bytes.Buffer
			subOffset := 0
			for _, sub := range encInfo.Subsamples {
				if sub.ClearBytes > 0 {
					decryptedBuf.Write(sampleBytes[subOffset : subOffset+int(sub.ClearBytes)])
					subOffset += int(sub.ClearBytes)
				}
				if sub.CipherBytes > 0 {
					cipherPart := sampleBytes[subOffset : subOffset+int(sub.CipherBytes)]
					subOffset += int(sub.CipherBytes)
					plainPart, err := decryptFn(kid, encInfo.IV, cipherPart)
					if err != nil {
						return nil, fmt.Errorf("failed to decrypt subsample in sample %d: %w", i, err)
					}
					decryptedBuf.Write(plainPart)
				}
			}
			decryptedSample = decryptedBuf.Bytes()
		}

		// Prepend ADTS header
		adtsHeader := MakeADTSHeader(len(decryptedSample), cfg)
		outBuf.Write(adtsHeader)
		outBuf.Write(decryptedSample)
	}

	return outBuf.Bytes(), nil
}
