// Package fitswrite writes minimal, single-HDU, raw instrument-native FITS
// files - the capture side of the two-program FITS contract
// (docs/SAUCEPAN_WHITE_PAPER.md §4): this package writes what a camera
// vendor's own software would, using the same raw header keywords
// normalize/header_map/synonyms.yaml already knows how to translate into
// the SP_* namespace (EXPTIME, DATE-OBS, RA, DEC, TELESCOP, INSTRUME,
// FILTER, GAIN, SITELAT, SITELONG, SITEELEV, PIXSCALE, OBJECT). It does not
// write SP_* headers itself and never will - that boundary belongs to
// normalize/, on the Python side, and duplicating it here would violate the
// two-program contract's "never touch pipeline code" rule.
//
// This is intentionally not a general-purpose FITS library: one primary
// HDU, one 2D image, BITPIX=32 (signed 32-bit, wide enough for any raw ADU
// range without the BZERO/BSCALE unsigned-16-bit convention some vendors
// use), no extensions, no compression, no multi-HDU support.
package fitswrite

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	blockSize = 2880
	cardSize  = 80
)

// Header is an ordered list of FITS header cards. Zero value is ready to
// use. SIMPLE/BITPIX/NAXIS*/END are added automatically by WriteImage and
// must not be set here.
type Header struct {
	cards []string // each already a fully formatted, unpadded (<=80 char) card
}

func NewHeader() *Header {
	return &Header{}
}

func (h *Header) SetString(key, value, comment string) {
	h.cards = append(h.cards, formatCard(key, quoteFITSString(value), comment))
}

func (h *Header) SetFloat(key string, value float64, comment string) {
	h.cards = append(h.cards, formatCard(key, formatFITSFloat(value), comment))
}

func (h *Header) SetInt(key string, value int, comment string) {
	h.cards = append(h.cards, formatCard(key, fmt.Sprintf("%20d", value), comment))
}

func (h *Header) SetBool(key string, value bool, comment string) {
	v := "F"
	if value {
		v = "T"
	}
	h.cards = append(h.cards, formatCard(key, fmt.Sprintf("%20s", v), comment))
}

// formatCard lays out one 80-column FITS card: keyword in columns 1-8,
// "= " in 9-10, value left-aligned from column 11, then " / comment" if
// given. Longer values/comments are truncated to fit - callers should keep
// values short (this package targets machine-generated instrument headers,
// not free-text).
func formatCard(key, value, comment string) string {
	key = strings.ToUpper(key)
	if len(key) > 8 {
		key = key[:8]
	}
	line := fmt.Sprintf("%-8s= %s", key, value)
	if comment != "" {
		line += " / " + comment
	}
	if len(line) > cardSize {
		line = line[:cardSize]
	}
	return line
}

// quoteFITSString applies the FITS single-quote string convention:
// embedded quotes doubled, minimum field width 8 (including the quotes).
func quoteFITSString(v string) string {
	escaped := strings.ReplaceAll(v, "'", "''")
	quoted := "'" + escaped + "'"
	for len(quoted) < 10 {
		quoted += " "
	}
	return quoted
}

func formatFITSFloat(v float64) string {
	s := strconv.FormatFloat(v, 'G', -1, 64)
	if len(s) < 20 {
		s = fmt.Sprintf("%20s", s)
	}
	return s
}

func endCard() string {
	return fmt.Sprintf("%-80s", "END")
}

func padToBlock(cards []string) []byte {
	var buf strings.Builder
	for _, c := range cards {
		buf.WriteString(fmt.Sprintf("%-80s", c))
	}
	remainder := buf.Len() % blockSize
	if remainder != 0 {
		buf.WriteString(strings.Repeat(" ", blockSize-remainder))
	}
	return []byte(buf.String())
}

// WriteImage writes a single-HDU FITS file at path: mandatory SIMPLE/
// BITPIX/NAXIS/NAXIS1/NAXIS2 cards, then extra's cards, then END, then the
// 2D data (row-major, data[y][x]) as big-endian int32, zero-padded to a
// FITS block boundary. Returns an error if data is empty or ragged (rows of
// differing length).
func WriteImage(path string, data [][]float64, extra *Header) error {
	height := len(data)
	if height == 0 {
		return fmt.Errorf("fitswrite: no rows in image data")
	}
	width := len(data[0])
	if width == 0 {
		return fmt.Errorf("fitswrite: zero-width image data")
	}
	for y, row := range data {
		if len(row) != width {
			return fmt.Errorf("fitswrite: ragged image data: row 0 has %d cols, row %d has %d", width, y, len(row))
		}
	}

	cards := []string{
		formatCard("SIMPLE", fmt.Sprintf("%20s", "T"), "conforms to FITS standard"),
		formatCard("BITPIX", fmt.Sprintf("%20d", 32), "32-bit signed integer"),
		formatCard("NAXIS", fmt.Sprintf("%20d", 2), "2-dimensional image"),
		formatCard("NAXIS1", fmt.Sprintf("%20d", width), "columns (x)"),
		formatCard("NAXIS2", fmt.Sprintf("%20d", height), "rows (y)"),
	}
	if extra != nil {
		cards = append(cards, extra.cards...)
	}
	cards = append(cards, endCard())

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("fitswrite: create %s: %w", path, err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	if _, err := w.Write(padToBlock(cards)); err != nil {
		return fmt.Errorf("fitswrite: write header: %w", err)
	}

	dataBytes := make([]byte, width*height*4)
	i := 0
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			binary.BigEndian.PutUint32(dataBytes[i:i+4], uint32(int32(data[y][x])))
			i += 4
		}
	}
	if _, err := w.Write(dataBytes); err != nil {
		return fmt.Errorf("fitswrite: write data: %w", err)
	}
	remainder := len(dataBytes) % blockSize
	if remainder != 0 {
		if _, err := w.Write(make([]byte, blockSize-remainder)); err != nil {
			return fmt.Errorf("fitswrite: pad data block: %w", err)
		}
	}

	return w.Flush()
}
