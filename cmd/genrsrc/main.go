// Generates a crisp Apple-style minimalist Shell.ico and embeds icon, manifest, and version info
// into rsrc_windows_amd64.syso in pure Go (no MSVC rc.exe / cvtres.exe required).
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
)

func main() {
	verStr := flag.String("version", "1.0.0", "SemVer version e.g. 1.0.0")
	outSyso := flag.String("out", "rsrc_windows_amd64.syso", "Output COFF .syso file")
	icoPath := flag.String("ico", "packaging/windows/Shell.ico", "Output multi-size ICO file")
	manifestPath := flag.String("manifest", "packaging/windows/Shell.manifest", "Windows manifest XML")
	flag.Parse()

	// 1. Render crisp macOS-style dark squircle icon with minimalist ">_" glyph at multiple sizes.
	sizes := []int{256, 128, 64, 48, 32, 16}
	pngFrames := make([][]byte, 0, len(sizes))
	var iconImages []image.Image
	for _, sz := range sizes {
		img := renderAppleShellIcon(sz)
		iconImages = append(iconImages, img)
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			log.Fatalf("encode png %d: %v", sz, err)
		}
		pngFrames = append(pngFrames, buf.Bytes())
	}

	if err := os.MkdirAll(filepath.Dir(*icoPath), 0755); err != nil {
		log.Fatal(err)
	}
	if err := writeICO(*icoPath, sizes, pngFrames); err != nil {
		log.Fatalf("write ico: %v", err)
	}

	// 2. Build Windows ResourceSet using pure-Go tc-hib/winres.
	rs := &winres.ResourceSet{}

	icon, err := winres.NewIconFromImages(iconImages)
	if err != nil {
		log.Fatalf("create winres icon: %v", err)
	}
	if err := rs.SetIcon(winres.ID(1), icon); err != nil {
		log.Fatalf("set icon: %v", err)
	}

	manifestBytes, err := os.ReadFile(*manifestPath)
	if err != nil {
		log.Fatalf("read manifest: %v", err)
	}
	if err := rs.Set(winres.RT_MANIFEST, winres.ID(1), 0x0409, manifestBytes); err != nil {
		log.Fatalf("set manifest: %v", err)
	}

	fv := parseVersion4(*verStr)
	vi := version.Info{
		FileVersion:    fv,
		ProductVersion: fv,
	}
	vi.Set(0x0409, version.FileDescription, "Shell — Minimalist SSH & SFTP Client")
	vi.Set(0x0409, version.ProductName, "Shell")
	vi.Set(0x0409, version.OriginalFilename, "Shell.exe")
	vi.Set(0x0409, version.InternalName, "Shell")
	vi.Set(0x0409, version.ProductVersion, *verStr)
	vi.Set(0x0409, version.FileVersion, fmt.Sprintf("%d.%d.%d.%d", fv[0], fv[1], fv[2], fv[3]))
	rs.SetVersionInfo(vi)

	out, err := os.Create(*outSyso)
	if err != nil {
		log.Fatalf("create syso: %v", err)
	}
	defer out.Close()
	if err := rs.WriteObject(out, winres.ArchAMD64); err != nil {
		log.Fatalf("write COFF object: %v", err)
	}
	fmt.Printf("Generated %s and %s (version %s)\n", *icoPath, *outSyso, *verStr)
}

func parseVersion4(v string) [4]uint16 {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if idx := strings.IndexByte(v, '-'); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.Split(v, ".")
	var out [4]uint16
	for i := 0; i < len(parts) && i < 4; i++ {
		n, err := strconv.Atoi(parts[i])
		if err == nil && n >= 0 && n <= 65535 {
			out[i] = uint16(n)
		}
	}
	if out == [4]uint16{} {
		out = [4]uint16{1, 0, 0, 0}
	}
	return out
}

func writeICO(filename string, sizes []int, frames [][]byte) error {
	var buf bytes.Buffer
	// ICONDIR header: reserved(0), type(1=ICO), count
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(frames)))

	offset := uint32(6 + 16*len(frames))
	for i, sz := range sizes {
		w := uint8(sz)
		h := uint8(sz)
		if sz >= 256 {
			w, h = 0, 0
		}
		buf.WriteByte(w)
		buf.WriteByte(h)
		buf.WriteByte(0) // color palette
		buf.WriteByte(0) // reserved
		_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
		_ = binary.Write(&buf, binary.LittleEndian, uint16(32))
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(frames[i])))
		_ = binary.Write(&buf, binary.LittleEndian, offset)
		offset += uint32(len(frames[i]))
	}
	for _, f := range frames {
		buf.Write(f)
	}
	return os.WriteFile(filename, buf.Bytes(), 0644)
}

// renderAppleShellIcon draws an antialiased macOS Big Sur / Sequoia squircle icon with subtle gradient,
// 1px metallic inner rim, and crisp ">_" terminal prompt glyph.
func renderAppleShellIcon(size int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	// 4x4 supersampling for silky smooth curves even at 16px and 32px.
	const ss = 4
	invSS := 1.0 / float64(ss)
	fSize := float64(size)

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var rSum, gSum, bSum, aSum float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					u := (float64(x) + (float64(sx)+0.5)*invSS) / fSize // [0,1]
					v := (float64(y) + (float64(sy)+0.5)*invSS) / fSize // [0,1]
					r, g, b, a := sampleIcon(u, v)
					rSum += r * a
					gSum += g * a
					bSum += b * a
					aSum += a
				}
			}
			a := aSum / float64(ss*ss)
			if a <= 0.0001 {
				continue
			}
			r := clampByte((rSum / aSum) * 255)
			g := clampByte((gSum / aSum) * 255)
			b := clampByte((bSum / aSum) * 255)
			img.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: clampByte(a * 255)})
		}
	}
	return img
}

func sampleIcon(u, v float64) (r, g, b, a float64) {
	// Apple superellipse (squircle) centered at (0.5, 0.5), radius 0.44, exponent n=5.
	nx := math.Abs(u-0.5) / 0.44
	ny := math.Abs(v-0.5) / 0.44
	d := math.Pow(math.Pow(nx, 5.0)+math.Pow(ny, 5.0), 1.0/5.0)
	if d > 1.0 {
		return 0, 0, 0, 0
	}

	// Base vibrant sapphire-cobalt vertical gradient (#3B82F6 -> #1D4ED8) — pops on both Light & Dark taskbars.
	t := math.Max(0, math.Min(1, (v-0.06)/0.88))
	r = lerp(0.231, 0.114, t)
	g = lerp(0.510, 0.306, t)
	b = lerp(0.965, 0.847, t)

	// Subtle top-edge specular glass sheen.
	if v < 0.48 {
		sheen := (1.0 - (v-0.06)/0.42) * 0.11
		if sheen > 0 {
			r = math.Min(1.0, r+sheen)
			g = math.Min(1.0, g+sheen)
			b = math.Min(1.0, b+sheen*0.8)
		}
	}

	// Crisp inner bevel rim.
	if d > 0.945 {
		rim := 0.22 * (1.0 - t*0.45)
		r = math.Min(1.0, r+rim)
		g = math.Min(1.0, g+rim)
		b = math.Min(1.0, b+rim)
	}

	// Draw crisp minimalist ">" chevron (left, pure white) and "_" cursor bar (right, luminous mint #6EE7B7).
	stroke := 0.042
	// Top leg of ">": (0.27, 0.36) -> (0.45, 0.50)
	d1 := distSeg(u, v, 0.27, 0.36, 0.45, 0.50)
	// Bottom leg of ">": (0.27, 0.64) -> (0.45, 0.50)
	d2 := distSeg(u, v, 0.27, 0.64, 0.45, 0.50)
	if math.Min(d1, d2) <= stroke {
		return 1.0, 1.0, 1.0, 1.0
	}
	// Cursor underscore bar: (0.53, 0.64) -> (0.72, 0.64) in luminous mint (#6EE7B7).
	d3 := distSeg(u, v, 0.53, 0.64, 0.72, 0.64)
	if d3 <= stroke*0.95 {
		return 0.431, 0.906, 0.718, 1.0
	}
	return r, g, b, 1.0
}

func distSeg(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return math.Hypot(px-ax, py-ay)
	}
	t := ((px-ax)*dx + (py-ay)*dy) / l2
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }
func clampByte(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(math.Round(v))
}
