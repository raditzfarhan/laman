package tray

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// The warning dot must sit clear of every part of the mark, with a gap of
// at least 8 units (one third of a stroke) so it reads as separate at 16 px.
func TestDotClearsMark(t *testing.T) {
	for y := 0.0; y < 256; y += 0.5 {
		for x := 0.0; x < 256; x += 0.5 {
			if math.Hypot(x-dotC.x, y-dotC.y) > dotR+8 {
				continue
			}
			for _, p := range append(markPolys, cursor) {
				if inPoly(p, x, y) {
					t.Fatalf("dot (plus gap) overlaps the mark at %.1f,%.1f", x, y)
				}
			}
		}
	}
	if dotC.x+dotR > 256 || dotC.y-dotR < 0 {
		t.Fatal("dot leaves the canvas")
	}
}

func TestICOHasAllSizes(t *testing.T) {
	ico := iconFor(running, false)
	if got := int(ico[4]) | int(ico[5])<<8; got != len(traySizes) {
		t.Fatalf("ico has %d images, want %d", got, len(traySizes))
	}
}

// LAMAN_ICON_PREVIEW=dir go test ./internal/tray -run Preview writes a sheet
// of every state on light and dark taskbars, at 1x and magnified.
func TestPreview(t *testing.T) {
	dir := os.Getenv("LAMAN_ICON_PREVIEW")
	if dir == "" {
		t.Skip("set LAMAN_ICON_PREVIEW to write previews")
	}
	const cell, zoom = 64, 4
	states := []status{running, warning, stopped}
	sheet := image.NewNRGBA(image.Rect(0, 0, 16*zoom*len(states)*2, cell+16*zoom))
	for row, light := range []bool{false, true} {
		bg := color.NRGBA{0x1c, 0x1c, 0x1c, 0xff}
		if light {
			bg = color.NRGBA{0xee, 0xee, 0xee, 0xff}
		}
		for i, s := range states {
			col := row*len(states) + i
			// Large preview at 48 px, and the 16 px icon magnified 4x below it.
			x0 := col * (16 * zoom)
			draw.Draw(sheet, image.Rect(x0, 0, x0+16*zoom, cell+16*zoom), &image.Uniform{bg}, image.Point{}, draw.Src)
			big := drawMark(48, s, light)
			draw.Draw(sheet, image.Rect(x0+8, 8, x0+56, 56), big, image.Point{}, draw.Over)
			small := drawMark(16, s, light)
			for y := 0; y < 16; y++ {
				for x := 0; x < 16; x++ {
					c := small.At(x, y)
					r := image.Rect(x0+x*zoom, cell+y*zoom, x0+(x+1)*zoom, cell+(y+1)*zoom)
					draw.Draw(sheet, r, &image.Uniform{c}, image.Point{}, draw.Over)
				}
			}
		}
	}
	f, err := os.Create(filepath.Join(dir, "tray-preview.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, sheet)
}
