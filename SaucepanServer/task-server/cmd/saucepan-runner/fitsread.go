package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

// frameData is the read-only view of the just-captured FITS the sandbox gets
// through the frame_stat host function. It is deliberately a small scalar
// surface, not the pixel array itself: researcher code on the pier reasons
// about "is there something on this frame" from summary statistics and a few
// header cards, not by shipping a megapixel buffer into wasm linear memory.
// Rich per-pixel access is a later tier (see ON_PIER_SANDBOX_RUNTIME.md §7).
type frameData struct {
	width, height int
	headers       map[string]float64 // numeric header cards, key upper-cased
	// pixel stats, computed once on load
	mean, median, min, max, std, sum float64
}

// readFrame parses the minimal single-HDU FITS that shared/fitswrite writes:
// 2880-byte header blocks of 80-column cards (SIMPLE, BITPIX=32, NAXIS=2,
// NAXIS1, NAXIS2, extras, END), then big-endian int32 row-major pixels,
// block-padded. It is not a general FITS reader — it matches exactly what the
// pier agent produces, nothing more.
func readFrame(path string) (*frameData, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read frame: %w", err)
	}
	const block, card = 2880, 80
	if len(raw) < block {
		return nil, fmt.Errorf("frame: shorter than one FITS block")
	}

	headers := map[string]float64{}
	var bitpix, naxis1, naxis2 int
	headerEnd := -1
	for off := 0; off+card <= len(raw); off += card {
		line := string(raw[off : off+card])
		key := strings.TrimSpace(line[:8])
		if key == "END" {
			// header spans whole 2880 blocks; data starts at the next boundary
			headerEnd = ((off / block) + 1) * block
			break
		}
		if len(line) < 10 || line[8] != '=' {
			continue
		}
		valPart := line[10:]
		if i := strings.Index(valPart, "/"); i >= 0 {
			valPart = valPart[:i]
		}
		valPart = strings.TrimSpace(valPart)
		if strings.HasPrefix(valPart, "'") {
			continue // string card — frame_stat only exposes numerics
		}
		v, err := strconv.ParseFloat(valPart, 64)
		if err != nil {
			continue
		}
		switch key {
		case "BITPIX":
			bitpix = int(v)
		case "NAXIS1":
			naxis1 = int(v)
		case "NAXIS2":
			naxis2 = int(v)
		}
		headers[key] = v
	}
	if headerEnd < 0 {
		return nil, fmt.Errorf("frame: no END card")
	}
	if bitpix != 32 {
		return nil, fmt.Errorf("frame: BITPIX %d unsupported (want 32)", bitpix)
	}
	if naxis1 <= 0 || naxis2 <= 0 {
		return nil, fmt.Errorf("frame: bad NAXIS1/NAXIS2 %d/%d", naxis1, naxis2)
	}

	n := naxis1 * naxis2
	need := headerEnd + n*4
	if len(raw) < need {
		return nil, fmt.Errorf("frame: truncated pixel data (have %d, need %d)", len(raw), need)
	}
	pix := make([]float64, n)
	for i := 0; i < n; i++ {
		u := binary.BigEndian.Uint32(raw[headerEnd+i*4:])
		pix[i] = float64(int32(u))
	}

	fd := &frameData{width: naxis1, height: naxis2, headers: headers}
	fd.computeStats(pix)
	return fd, nil
}

func (fd *frameData) computeStats(pix []float64) {
	if len(pix) == 0 {
		return
	}
	fd.min, fd.max = pix[0], pix[0]
	for _, v := range pix {
		fd.sum += v
		if v < fd.min {
			fd.min = v
		}
		if v > fd.max {
			fd.max = v
		}
	}
	fd.mean = fd.sum / float64(len(pix))
	var ss float64
	for _, v := range pix {
		d := v - fd.mean
		ss += d * d
	}
	fd.std = math.Sqrt(ss / float64(len(pix)))

	cp := append([]float64(nil), pix...)
	sort.Float64s(cp)
	if len(cp)%2 == 1 {
		fd.median = cp[len(cp)/2]
	} else {
		fd.median = 0.5 * (cp[len(cp)/2-1] + cp[len(cp)/2])
	}
}

// stat resolves one frame_stat key. Unknown keys return NaN so the guest can
// test with math.IsNaN and never gets a misleading zero.
func (fd *frameData) stat(key string) float64 {
	switch strings.ToLower(key) {
	case "width":
		return float64(fd.width)
	case "height":
		return float64(fd.height)
	case "npix":
		return float64(fd.width * fd.height)
	case "mean":
		return fd.mean
	case "median":
		return fd.median
	case "min":
		return fd.min
	case "max":
		return fd.max
	case "std":
		return fd.std
	case "sum":
		return fd.sum
	}
	if h, ok := strings.CutPrefix(key, "hdr:"); ok {
		if v, ok := fd.headers[strings.ToUpper(h)]; ok {
			return v
		}
	}
	return math.NaN()
}
