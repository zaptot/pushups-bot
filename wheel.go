package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/png"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	wheelSize    = 320
	wheelDiscMin = 26 // квадрат вокруг колеса: только он меняется от кадра к кадру
	wheelDiscMax = wheelSize - 26
	wheelFrames  = 105
	wheelFrameCs = 3   // кадр, сотые секунды (задержки ≤ 10 мс браузеры замедляют до 100 мс)
	wheelHoldCs  = 150 // пауза на последнем кадре
	wheelTurns   = 4
	wheelTimeout = 15 * time.Second // на рисование и на каждую загрузку, потом результат без анимации
)

var (
	wheelSpinTime = (wheelFrames*wheelFrameCs+wheelHoldCs)*10*time.Millisecond + time.Second // +1 с на загрузку у зрителей

	wheelBG     = color.RGBA{35, 36, 40, 255}
	wheelYellow = color.RGBA{250, 204, 21, 255}
	wheelRed    = color.RGBA{220, 38, 38, 255}
	wheelRim    = color.RGBA{17, 18, 20, 255}
	wheelArrow  = color.RGBA{245, 245, 245, 255}
	wheelWhite  = color.RGBA{255, 255, 255, 255}

	wheelMultipliers = []int{2, 3, 5, 10} // шанс выигрыша — 1 / множитель
)

type wheelArt struct {
	base    *image.RGBA // колесо на фоне
	overlay *image.RGBA // стрелка и ось, альфа умножена на цвет
	palette color.Palette
	nearest []uint8 // индекс палитры по цвету, 5 бит на канал
}

var (
	wheelCacheMu sync.Mutex
	wheelCache   = map[int]*wheelArt{}
)

func wheelArtFor(coef int) (*wheelArt, error) {
	wheelCacheMu.Lock()
	defer wheelCacheMu.Unlock()
	if art, ok := wheelCache[coef]; ok {
		return art, nil
	}
	art, err := drawWheelArt(coef)
	if err == nil {
		wheelCache[coef] = art
	}
	return art, err
}

// drawWheelArt рисует колесо (жёлтый сектор 1/coef по часовой от верха) вдвое крупнее и сжимает.
func drawWheelArt(coef int) (*wheelArt, error) {
	const s = wheelSize * 2
	c, r := float64(s)/2, float64(s)/2-56
	span := 360 / float64(coef)

	big := image.NewRGBA(image.Rect(0, 0, s, s))
	for y := range s {
		for x := range s {
			dx, dy := float64(x)+0.5-c, float64(y)+0.5-c
			dist := math.Hypot(dx, dy)
			col := wheelBG
			if dist <= r {
				// градусы по часовой от верха
				if a := math.Mod(math.Atan2(dx, -dy)*180/math.Pi+360, 360); a < span {
					col = wheelYellow
				} else {
					col = wheelRed
				}
				if dist >= r-14 || onTick(dx, dy, r) {
					col = wheelRim
				}
			}
			big.SetRGBA(x, y, col)
		}
	}
	size := 80.0
	labelAt := 0.55
	if coef >= 5 {
		size, labelAt = 58, 0.72
	}
	face, err := wheelFace(size)
	if err != nil {
		return nil, err
	}
	defer face.Close()
	drawLabel(big, face, fmt.Sprintf("x%d", coef), -90+span/2, r*labelAt, c, wheelRim)
	drawLabel(big, face, "x0", -90+span+(360-span)/2, r*0.55, c, wheelWhite)

	over := image.NewRGBA(image.Rect(0, 0, s, s))
	a, b, t := [2]float64{c - 36, 6}, [2]float64{c + 36, 6}, [2]float64{c, 120}
	for y := range s {
		for x := range s {
			px, py := float64(x)+0.5, float64(y)+0.5
			switch {
			case math.Hypot(px-c, py-c) <= 40:
				over.SetRGBA(x, y, wheelRim)
			case inTriangle(px, py, a, b, t):
				if segDist(px, py, a, b) <= 2 || segDist(px, py, b, t) <= 2 || segDist(px, py, t, a) <= 2 {
					over.SetRGBA(x, y, wheelRim)
				} else {
					over.SetRGBA(x, y, wheelArrow)
				}
			}
		}
	}

	art := &wheelArt{base: halve(big), overlay: halve(over)}
	art.palette = wheelPalette()
	art.nearest = make([]uint8, 1<<15)
	for i := range art.nearest {
		r5, g5, b5 := i>>10&31, i>>5&31, i&31
		art.nearest[i] = uint8(closestOpaque(art.palette, uint8(r5<<3|r5>>2), uint8(g5<<3|g5>>2), uint8(b5<<3|b5>>2)))
	}
	return art, nil
}

func onTick(dx, dy, r float64) bool {
	for i := range 24 {
		a := float64(i) * 15 * math.Pi / 180
		inner := r - 20
		if i%2 == 1 {
			inner = r - 36
		}
		p := [2]float64{inner * math.Cos(a), inner * math.Sin(a)}
		q := [2]float64{r * math.Cos(a), r * math.Sin(a)}
		if segDist(dx, dy, p, q) <= 3 {
			return true
		}
	}
	return false
}

func segDist(x, y float64, a, b [2]float64) float64 {
	vx, vy := b[0]-a[0], b[1]-a[1]
	t := ((x-a[0])*vx + (y-a[1])*vy) / (vx*vx + vy*vy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(x-(a[0]+t*vx), y-(a[1]+t*vy))
}

func inTriangle(x, y float64, a, b, c [2]float64) bool {
	side := func(p, q [2]float64) float64 { return (x-q[0])*(p[1]-q[1]) - (p[0]-q[0])*(y-q[1]) }
	d1, d2, d3 := side(a, b), side(b, c), side(c, a)
	return !((d1 < 0 || d2 < 0 || d3 < 0) && (d1 > 0 || d2 > 0 || d3 > 0))
}

func wheelFace(size float64) (font.Face, error) {
	f, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
}

func drawLabel(img *image.RGBA, face font.Face, text string, midDeg, dist, c float64, col color.RGBA) {
	a := midDeg * math.Pi / 180
	d := &font.Drawer{Dst: img, Src: image.NewUniform(col), Face: face}
	width := float64(d.MeasureString(text)) / 64
	m := face.Metrics()
	height := float64(m.Ascent-m.Descent) / 64
	x := c + dist*math.Cos(a) - width/2
	y := c + dist*math.Sin(a) + height/2
	d.Dot = fixed.P(int(x), int(y))
	d.DrawString(text)
}

// halve уменьшает вдвое усреднением четырёх пикселей (сглаживание); альфа остаётся умноженной на цвет.
func halve(src *image.RGBA) *image.RGBA {
	w, h := src.Rect.Dx()/2, src.Rect.Dy()/2
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			var sum [4]int
			for _, p := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
				o := src.PixOffset(2*x+p[0], 2*y+p[1])
				for i := range sum {
					sum[i] += int(src.Pix[o+i])
				}
			}
			o := dst.PixOffset(x, y)
			for i := range sum {
				dst.Pix[o+i] = uint8(sum[i] / 4)
			}
		}
	}
	return dst
}

// wheelPalette: прозрачный цвет (для неизменившихся пикселей), основные цвета и переходы между ними.
func wheelPalette() color.Palette {
	pal := color.Palette{color.RGBA{}}
	base := []color.RGBA{wheelBG, wheelYellow, wheelRed, wheelRim, wheelArrow}
	pal = append(pal, wheelBG, wheelYellow, wheelRed, wheelRim, wheelArrow)
	for i, a := range base {
		for _, b := range base[i+1:] {
			for k := 1; k <= 5; k++ {
				pal = append(pal, mix(a, b, float64(k)/6))
			}
		}
	}
	return pal
}

func mix(a, b color.RGBA, t float64) color.RGBA {
	f := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t + 0.5) }
	return color.RGBA{f(a.R, b.R), f(a.G, b.G), f(a.B, b.B), 255}
}

func closestOpaque(pal color.Palette, r, g, b uint8) int {
	best, bestDist := 1, 1<<31-1
	for i := 1; i < len(pal); i++ {
		c := pal[i].(color.RGBA)
		dr, dg, db := int(c.R)-int(r), int(c.G)-int(g), int(c.B)-int(b)
		if d := dr*dr + dg*dg + db*db; d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

// pickSpin: начальный угол колеса и куда укажет стрелка (градусы по часовой от верха, 0–span — жёлтый сектор).
func pickSpin(coef int, win bool) (start, pointer float64) {
	span := 360 / float64(coef)
	margin := math.Min(15, span/4) // чтобы стрелка не вставала на границу секторов
	if win {
		pointer = margin + rand.Float64()*(span-2*margin)
	} else {
		pointer = span + margin + rand.Float64()*(360-span-2*margin)
	}
	return rand.Float64() * 360, pointer
}

// renderFrame рисует колесо, повёрнутое по часовой на angle градусов.
func renderFrame(art *wheelArt, angle float64, dst *image.RGBA) {
	sin, cos := math.Sincos(angle * math.Pi / 180)
	const center = float64(wheelSize) / 2
	base := art.base
	for y := wheelDiscMin; y < wheelDiscMax; y++ {
		for x := wheelDiscMin; x < wheelDiscMax; x++ {
			dx, dy := float64(x)+0.5-center, float64(y)+0.5-center
			sx, sy := dx*cos+dy*sin+center-0.5, -dx*sin+dy*cos+center-0.5
			x0, y0 := int(math.Floor(sx)), int(math.Floor(sy))
			fx, fy := sx-float64(x0), sy-float64(y0)
			o := dst.PixOffset(x, y)
			if x0 < 0 || y0 < 0 || x0+1 >= wheelSize || y0+1 >= wheelSize {
				copy(dst.Pix[o:o+4], []uint8{wheelBG.R, wheelBG.G, wheelBG.B, 255})
				continue
			}
			a, b := base.PixOffset(x0, y0), base.PixOffset(x0+1, y0)
			c, d := base.PixOffset(x0, y0+1), base.PixOffset(x0+1, y0+1)
			for i := range 3 {
				top := float64(base.Pix[a+i])*(1-fx) + float64(base.Pix[b+i])*fx
				bot := float64(base.Pix[c+i])*(1-fx) + float64(base.Pix[d+i])*fx
				dst.Pix[o+i] = uint8(top*(1-fy) + bot*fy + 0.5)
			}
			dst.Pix[o+3] = 255
		}
	}
	draw.Draw(dst, dst.Rect, art.overlay, image.Point{}, draw.Over)
}

// renderWheel возвращает анимацию GIF и финальный кадр PNG.
func renderWheel(coef int, start, pointer float64) (gifData, pngData []byte, err error) {
	art, err := wheelArtFor(coef)
	if err != nil {
		return nil, nil, err
	}
	distance := float64(360*wheelTurns) + math.Mod(math.Mod(360-pointer-start, 360)+360, 360)

	canvas := image.NewRGBA(image.Rect(0, 0, wheelSize, wheelSize))
	draw.Draw(canvas, canvas.Rect, image.NewUniform(wheelBG), image.Point{}, draw.Src)

	anim := &gif.GIF{
		LoopCount: -1, // без повторов
		Config:    image.Config{ColorModel: art.palette, Width: wheelSize, Height: wheelSize},
	}
	var prev *image.Paletted
	for i := range wheelFrames {
		t := float64(i) / float64(wheelFrames-1)
		renderFrame(art, start+distance*(1-math.Pow(1-t, 3)), canvas)
		frame := paletted(art, canvas)
		out := frame
		if prev != nil {
			// первый кадр целиком, в остальных только изменившаяся часть: неизменные пиксели прозрачны
			out = image.NewPaletted(image.Rect(wheelDiscMin, 0, wheelDiscMax, wheelDiscMax), art.palette)
			for y := out.Rect.Min.Y; y < out.Rect.Max.Y; y++ {
				for x := out.Rect.Min.X; x < out.Rect.Max.X; x++ {
					if v := frame.ColorIndexAt(x, y); v != prev.ColorIndexAt(x, y) {
						out.SetColorIndex(x, y, v)
					}
				}
			}
		}
		prev = frame
		anim.Image = append(anim.Image, out)
		anim.Delay = append(anim.Delay, wheelFrameCs)
		anim.Disposal = append(anim.Disposal, gif.DisposalNone)
	}
	anim.Delay[len(anim.Delay)-1] = wheelHoldCs

	var gifBuf, pngBuf bytes.Buffer
	if err := gif.EncodeAll(&gifBuf, anim); err != nil {
		return nil, nil, err
	}
	if err := png.Encode(&pngBuf, canvas); err != nil {
		return nil, nil, err
	}
	return gifBuf.Bytes(), pngBuf.Bytes(), nil
}

func paletted(art *wheelArt, src *image.RGBA) *image.Paletted {
	dst := image.NewPaletted(src.Rect, art.palette)
	for i := 0; i < len(src.Pix); i += 4 {
		key := int(src.Pix[i])>>3<<10 | int(src.Pix[i+1])>>3<<5 | int(src.Pix[i+2])>>3
		dst.Pix[i/4] = art.nearest[key]
	}
	return dst
}
