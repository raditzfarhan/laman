package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// The tray mark is "Rumah Prompt" from logo/kit (laman-tray-light.svg and
// laman-tray-dark.svg): a roof with crossed gable ends over a `>` and a
// Genting cursor. The polygons below are that file's geometry on its
// 256 grid, copied exactly. Do not adjust them by eye; regenerate them
// with logo/kit/source/kit.py instead.
//
// State is shown without covering the mark: running is the plain mark,
// warning adds an amber dot in the empty corner above the right rafter,
// and stopped greys the whole mark.

type status int

const (
	stopped status = iota
	running
	warning
)

type pt struct{ x, y float64 }

var (
	markPolys = [][]pt{
		{{37.93, 164.74}, {168.00, 34.67}, {141.33, 8.00}, {11.26, 138.07}},                                          // left rafter
		{{88.00, 34.67}, {218.07, 164.74}, {244.74, 138.07}, {114.67, 8.00}},                                         // right rafter
		{{114.67, 148.00}, {151.33, 184.67}, {114.67, 221.33}, {141.33, 248.00}, {204.67, 184.67}, {141.33, 121.33}}, // >
	}
	cursor = []pt{{216.45, 210.29}, {244.73, 210.29}, {244.73, 248.00}, {216.45, 248.00}}

	// The dot sits in the triangle above the right rafter's top edge
	// (y = x - 106.67), centred 46 units from it, so it never touches the mark.
	dotC = pt{214, 42}
	dotR = 34.0

	malam       = color.NRGBA{0x14, 0x21, 0x3d, 0xff}
	white       = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	genting     = color.NRGBA{0xe2, 0x58, 0x3e, 0xff}
	amber       = color.NRGBA{0xf2, 0xa9, 0x00, 0xff}
	greyOnLight = color.NRGBA{0x8a, 0x93, 0x9f, 0xff}
	greyOnDark  = color.NRGBA{0x9a, 0xa0, 0xa8, 0xff}
)

// traySizes covers 100% to 300% display scaling; Windows picks the best fit.
var traySizes = []int{16, 20, 24, 32, 40, 48}

// iconFor returns an .ico for the state, drawn for a light or dark taskbar.
func iconFor(s status, lightTaskbar bool) []byte {
	imgs := make([]image.Image, len(traySizes))
	for i, n := range traySizes {
		imgs[i] = drawMark(n, s, lightTaskbar)
	}
	return toICO(imgs)
}

func drawMark(n int, s status, light bool) image.Image {
	body, cur := white, genting
	if light {
		body = malam
	}
	if s == stopped {
		body = greyOnDark
		if light {
			body = greyOnLight
		}
		cur = body
	}
	type layer struct {
		c      color.NRGBA
		inside func(x, y float64) bool
	}
	layers := []layer{
		{body, func(x, y float64) bool {
			for _, p := range markPolys {
				if inPoly(p, x, y) {
					return true
				}
			}
			return false
		}},
		{cur, func(x, y float64) bool { return inPoly(cursor, x, y) }},
	}
	if s == warning {
		layers = append(layers, layer{amber, func(x, y float64) bool { return math.Hypot(x-dotC.x, y-dotC.y) <= dotR }})
	}

	const ss = 4 // 4x4 supersampling per pixel
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	scale := 256 / float64(n)
	for py := 0; py < n; py++ {
		for px := 0; px < n; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) * scale
					y := (float64(py) + (float64(sy)+0.5)/ss) * scale
					for i := len(layers) - 1; i >= 0; i-- {
						if layers[i].inside(x, y) {
							c := layers[i].c
							r, g, b, a = r+float64(c.R), g+float64(c.G), b+float64(c.B), a+1
							break
						}
					}
				}
			}
			if a == 0 {
				continue
			}
			// Average colour over covered samples; coverage becomes alpha.
			img.SetNRGBA(px, py, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(math.Round(a / (ss * ss) * 255))})
		}
	}
	return img
}

// inPoly is the even-odd ray-crossing test.
func inPoly(p []pt, x, y float64) bool {
	in := false
	for i, j := 0, len(p)-1; i < len(p); j, i = i, i+1 {
		if (p[i].y > y) != (p[j].y > y) && x < (p[j].x-p[i].x)*(y-p[i].y)/(p[j].y-p[i].y)+p[i].x {
			in = !in
		}
	}
	return in
}

// toICO packs PNG images into one .ico, which Windows Vista+ accepts.
func toICO(imgs []image.Image) []byte {
	pngs := make([][]byte, len(imgs))
	for i, img := range imgs {
		var buf bytes.Buffer
		png.Encode(&buf, img)
		pngs[i] = buf.Bytes()
	}
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	w(uint16(0))         // reserved
	w(uint16(1))         // type: icon
	w(uint16(len(imgs))) // image count
	offset := 6 + 16*len(imgs)
	for i, img := range imgs {
		w(uint8(img.Bounds().Dx() % 256)) // width (0 means 256)
		w(uint8(img.Bounds().Dy() % 256))
		w(uint8(0))  // palette
		w(uint8(0))  // reserved
		w(uint16(1)) // planes
		w(uint16(32))
		w(uint32(len(pngs[i])))
		w(uint32(offset))
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		b.Write(p)
	}
	return b.Bytes()
}
