package xiaomidrive

import (
	"strings"

	"rsc.io/qr"
)

// RenderQR returns a Unicode-block rendering of text as a QR code, suitable for
// printing to a terminal. Two code rows are packed into each text line using
// half-block characters, with a one-module quiet zone.
func RenderQR(text string) (string, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", err
	}
	const quiet = 2
	size := code.Size
	full := size + quiet*2

	black := func(x, y int) bool {
		if x < quiet || y < quiet || x >= quiet+size || y >= quiet+size {
			return false // quiet zone is white
		}
		return code.Black(x-quiet, y-quiet)
	}

	var b strings.Builder
	for y := 0; y < full; y += 2 {
		for x := 0; x < full; x++ {
			top := black(x, y)
			bottom := y+1 < full && black(x, y+1)
			switch {
			case top && bottom:
				b.WriteRune('█')
			case top && !bottom:
				b.WriteRune('▀')
			case !top && bottom:
				b.WriteRune('▄')
			default:
				b.WriteByte(' ')
			}
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}
