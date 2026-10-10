package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

// mark is drawn over the logo's bottom-right dots.
type mark int

const (
	plain mark = iota
	arrow      // an exit node is in use
	cross      // the exit node in use is offline
)

// palette colors the logo. On macOS only the alpha counts, since the
// menu bar draws the icon as a template.
type palette struct {
	lit, dim, mark, cross color.NRGBA
}

var (
	mono = palette{
		lit:   color.NRGBA{A: 255},
		dim:   color.NRGBA{A: 90},
		mark:  color.NRGBA{A: 255},
		cross: color.NRGBA{A: 255},
	}
	dark = palette{
		lit:   color.NRGBA{255, 255, 255, 255},
		dim:   color.NRGBA{102, 102, 102, 255},
		mark:  color.NRGBA{255, 255, 255, 255},
		cross: color.NRGBA{229, 111, 74, 255},
	}
	light = palette{
		lit:   color.NRGBA{0, 0, 0, 255},
		dim:   color.NRGBA{153, 153, 153, 255},
		mark:  color.NRGBA{0, 0, 0, 255},
		cross: color.NRGBA{229, 111, 74, 255},
	}
	green = palette{
		lit: color.NRGBA{52, 199, 89, 255},
		dim: color.NRGBA{52, 199, 89, 90},
	}

	onlineColor  = color.NRGBA{52, 199, 89, 255}
	offlineColor = color.NRGBA{142, 142, 147, 255}
)

var (
	connectedDots = [9]bool{
		false, false, false,
		true, true, true,
		false, true, false,
	}
	// ring is the order the connecting animation lights the outer dots in.
	ring = [8]int{0, 1, 2, 5, 8, 7, 6, 3}
)

// icon is one state of the menu bar logo, as a macOS template image and as
// a colored image for Linux trays.
type icon struct {
	template, regular []byte
}

type icons struct {
	disconnected, connected, exitNode, exitOffline icon
	connecting                                     [len(ring)]icon
	copied                                         icon
	online, offline                                []byte
}

func newIcons(p palette) (icons, error) {
	var e pngs
	render := func(lit [9]bool, m mark) icon {
		return icon{template: e.encode(logo(lit, m, mono)), regular: e.encode(logo(lit, m, p))}
	}
	is := icons{
		disconnected: render([9]bool{}, plain),
		connected:    render(connectedDots, plain),
		exitNode:     render(connectedDots, arrow),
		exitOffline:  render(connectedDots, cross),
		copied:       icon{regular: e.encode(logo(connectedDots, plain, green))},
		online:       e.encode(disc(onlineColor)),
		offline:      e.encode(disc(offlineColor)),
	}
	for i, d := range ring {
		var lit [9]bool
		lit[d] = true
		is.connecting[i] = render(lit, plain)
	}
	if e.err != nil {
		return icons{}, fmt.Errorf("drawing icons: %w", e.err)
	}
	return is, nil
}

// logoPx is the rendered size; both menu bars scale it down, so this stays
// sharp on high-density screens.
const logoPx = 64

// logo draws the 3×3 dot grid in a 10×10 grid of dot radii: dots of radius
// one, centered at 2, 5 and 8, with a mark over the bottom right.
func logo(lit [9]bool, m mark, p palette) *image.NRGBA {
	const r = logoPx / 10.0
	return raster(logoPx, func(x, y float64) (color.NRGBA, bool) {
		return logoAt(x/r, y/r, lit, m, p)
	})
}

func logoAt(u, v float64, lit [9]bool, m mark, p palette) (color.NRGBA, bool) {
	switch m {
	case arrow:
		if nearArrow(u, v, 0.5) {
			return p.mark, true
		}
		if nearArrow(u, v, 1.5) {
			return color.NRGBA{}, false
		}
	case cross:
		if nearCross(u, v, 0.5) {
			return p.cross, true
		}
		if u >= 4 && v >= 4 {
			return color.NRGBA{}, false
		}
	}
	for i, on := range lit {
		cx, cy := float64(2+3*(i%3)), float64(2+3*(i/3))
		if dx, dy := u-cx, v-cy; dx*dx+dy*dy > 1 {
			continue
		}
		if on {
			return p.lit, true
		}
		return p.dim, true
	}
	return color.NRGBA{}, false
}

func nearArrow(u, v, w float64) bool {
	return nearSegment(u, v, 4.5, 8, 9.5, 8, w) ||
		nearSegment(u, v, 8, 6.5, 9.5, 8, w) ||
		nearSegment(u, v, 8, 9.5, 9.5, 8, w)
}

func nearCross(u, v, w float64) bool {
	return nearSegment(u, v, 5, 5, 8.5, 8.5, w) || nearSegment(u, v, 5, 8.5, 8.5, 5, w)
}

func nearSegment(u, v, ax, ay, bx, by, w float64) bool {
	dx, dy := bx-ax, by-ay
	t := max(0, min(1, ((u-ax)*dx+(v-ay)*dy)/(dx*dx+dy*dy)))
	ex, ey := u-(ax+t*dx), v-(ay+t*dy)
	return ex*ex+ey*ey <= w*w
}

func disc(c color.NRGBA) *image.NRGBA {
	const px, r = 32, 32 * 0.28
	return raster(px, func(x, y float64) (color.NRGBA, bool) {
		dx, dy := x-px/2, y-px/2
		return c, dx*dx+dy*dy <= r*r
	})
}

// raster renders shape as an antialiased px×px image.
func raster(px int, shape func(x, y float64) (color.NRGBA, bool)) *image.NRGBA {
	const ss = 4
	img := image.NewNRGBA(image.Rect(0, 0, px, px))
	for y := range px {
		for x := range px {
			var r, g, b, a float64
			for sy := range ss {
				for sx := range ss {
					c, ok := shape(float64(x)+(float64(sx)+0.5)/ss, float64(y)+(float64(sy)+0.5)/ss)
					if !ok {
						continue
					}
					ca := float64(c.A) / 255
					r += float64(c.R) * ca
					g += float64(c.G) * ca
					b += float64(c.B) * ca
					a += ca
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(255 * a / ss / ss)})
		}
	}
	return img
}

// pngs encodes images in turn with one compressor, which png.Encode would
// allocate anew, about a megabyte, for each. It keeps the first error, so a
// run of encodes is checked once at the end.
type pngs struct {
	buf *png.EncoderBuffer
	err error
}

func (e *pngs) encode(img image.Image) []byte {
	if e.err != nil {
		return nil
	}
	var b bytes.Buffer
	enc := png.Encoder{BufferPool: e}
	e.err = enc.Encode(&b, img)
	return b.Bytes()
}

// Get returns nil until the first encode, which makes png allocate the
// buffer that Put then keeps.
func (e *pngs) Get() *png.EncoderBuffer { return e.buf }

func (e *pngs) Put(b *png.EncoderBuffer) { e.buf = b }
