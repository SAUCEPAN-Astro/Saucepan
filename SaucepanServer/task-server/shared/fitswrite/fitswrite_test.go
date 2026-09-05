package fitswrite

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parseHeaderBlocks reads raw FITS bytes and returns the 80-char cards up
// to and including END, plus the total header byte length (a multiple of
// blockSize). No astropy/Python dependency - this package's own contract
// (card layout, block alignment) is verified directly, matching this
// repo's hermetic Go test convention.
func parseHeaderBlocks(t *testing.T, raw []byte) (cards []string, headerLen int) {
	t.Helper()
	if len(raw)%blockSize != 0 {
		t.Fatalf("file length %d is not a multiple of blockSize %d before any data starts", len(raw), blockSize)
	}
	for offset := 0; offset+blockSize <= len(raw); offset += blockSize {
		block := raw[offset : offset+blockSize]
		for i := 0; i+cardSize <= len(block); i += cardSize {
			card := string(block[i : i+cardSize])
			cards = append(cards, card)
			if strings.HasPrefix(card, "END") {
				return cards, offset + blockSize
			}
		}
	}
	t.Fatal("no END card found within file")
	return nil, 0
}

func TestWriteImage_HeaderStructure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capture.fits")

	extra := NewHeader()
	extra.SetFloat("RA", 83.8221, "target RA, degrees")
	extra.SetFloat("DEC", -5.3911, "target Dec, degrees")
	extra.SetString("TELESCOP", "node-a-20cm", "telescope id")
	extra.SetString("FILTER", "Ha", "filter name")
	extra.SetInt("GAIN", 100, "camera gain")

	data := [][]float64{
		{10, 20, 30},
		{40, 50, 60},
	}
	if err := WriteImage(path, data, extra); err != nil {
		t.Fatalf("WriteImage: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}

	cards, headerLen := parseHeaderBlocks(t, raw)
	if headerLen%blockSize != 0 {
		t.Fatalf("header length %d not block-aligned", headerLen)
	}

	if !strings.HasPrefix(cards[0], "SIMPLE  = ") || !strings.Contains(cards[0], "T") {
		t.Fatalf("card 0 = %q; want SIMPLE = T first", cards[0])
	}
	joined := strings.Join(cards, "\n")
	for _, want := range []string{"BITPIX", "NAXIS1", "NAXIS2", "RA", "DEC", "TELESCOP", "FILTER", "GAIN"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected header to contain %s, cards:\n%s", want, joined)
		}
	}
	if cards[len(cards)-1] != strings.Repeat(" ", 77)+"" && !strings.HasPrefix(cards[len(cards)-1], "END") {
		t.Errorf("last parsed card should be END, got %q", cards[len(cards)-1])
	}

	// Data block: 2 rows x 3 cols x 4 bytes/pixel (int32), padded to blockSize.
	dataBytes := raw[headerLen:]
	wantDataLen := blockSize // 24 raw bytes rounds up to one full block
	if len(dataBytes) != wantDataLen {
		t.Fatalf("data section length = %d, want %d (one padded block)", len(dataBytes), wantDataLen)
	}

	// Spot-check pixel values round-trip as big-endian int32.
	px := func(i int) int32 { return int32(binary.BigEndian.Uint32(dataBytes[i*4 : i*4+4])) }
	wantFlat := []int32{10, 20, 30, 40, 50, 60}
	for i, want := range wantFlat {
		if got := px(i); got != want {
			t.Errorf("pixel %d = %d, want %d", i, got, want)
		}
	}
}

func TestWriteImage_RejectsRaggedRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.fits")
	data := [][]float64{{1, 2, 3}, {4, 5}}
	if err := WriteImage(path, data, nil); err == nil {
		t.Fatal("expected an error for ragged row lengths, got nil")
	}
}

func TestWriteImage_RejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.fits")
	if err := WriteImage(path, nil, nil); err == nil {
		t.Fatal("expected an error for empty image data, got nil")
	}
}

func TestQuoteFITSString_EscapesEmbeddedQuotes(t *testing.T) {
	q := quoteFITSString("O'Brien")
	if !strings.Contains(q, "''") {
		t.Errorf("quoteFITSString(%q) = %q; want doubled embedded quote", "O'Brien", q)
	}
}
