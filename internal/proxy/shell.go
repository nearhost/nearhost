package proxy

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"strconv"
	"sync"
	"text/template"

	htmltemplate "html/template"

	"github.com/nearhost/nearhost/internal/report"
)

// The shell is what lets a browser install the page as an app: a manifest,
// the icons, a service worker and the offline page it falls back to. All of
// it is the same for every visitor, so it is served without the gate and
// never varies with cookies.

//go:embed home/sw.js
var swJS string

//go:embed home/offline.html
var offlineHTML string

var (
	swTemplate      = template.Must(template.New("sw").Parse(swJS))
	offlineTemplate = htmltemplate.Must(htmltemplate.New("offline").Parse(offlineHTML))
)

// shellAsset is one file of the shell.
type shellAsset struct {
	contentType string
	body        func(h *Home) []byte
}

// shellAssets maps each shell path to its file.
var shellAssets = map[string]shellAsset{
	"/manifest.webmanifest": {"application/manifest+json", func(h *Home) []byte { return h.manifest }},
	"/sw.js":                {"text/javascript; charset=utf-8", func(h *Home) []byte { return h.sw }},
	"/offline.html":         {"text/html; charset=utf-8", func(h *Home) []byte { return h.offline }},
	"/icon-180.png":         {"image/png", func(*Home) []byte { return markPNG(180) }},
	"/icon-192.png":         {"image/png", func(*Home) []byte { return markPNG(192) }},
	"/icon-512.png":         {"image/png", func(*Home) []byte { return markPNG(512) }},
}

// renderShell renders the files of the shell that depend on the version and
// the machine's name.
func (h *Home) renderShell() error {
	v := h.Version
	if v == "" {
		v = "dev"
	}
	data := struct{ Version, Machine string }{v, h.Machine}
	var sw, offline bytes.Buffer
	if err := swTemplate.Execute(&sw, data); err != nil {
		return err
	}
	if err := offlineTemplate.Execute(&offline, data); err != nil {
		return err
	}
	name := "Nearhost"
	if h.Machine != "" {
		name = "Nearhost on " + h.Machine
	}
	type icon struct {
		Src   string `json:"src"`
		Sizes string `json:"sizes"`
		Type  string `json:"type"`
	}
	manifest, err := json.MarshalIndent(map[string]any{
		"name":             name,
		"short_name":       "Nearhost",
		"display":          "standalone",
		"start_url":        "/",
		"scope":            "/",
		"background_color": "#0F1210",
		"theme_color":      "#0F1210",
		"icons": []icon{
			{"/icon-192.png", "192x192", "image/png"},
			{"/icon-512.png", "512x512", "image/png"},
		},
	}, "", "  ")
	if err != nil {
		return err
	}
	h.sw, h.offline, h.manifest = sw.Bytes(), offline.Bytes(), manifest
	return nil
}

// serveShell answers a request for one of the shell's paths at a page host,
// GET and HEAD only, and reports whether it answered. No cookie is needed,
// given or varied on: the files are the same for everyone.
func (h *Home) serveShell(w http.ResponseWriter, r *http.Request) bool {
	asset, ok := shellAssets[r.URL.Path]
	if !ok || !(atPageHost(r) || requestHost(r) == homeHost || h.atNetworkHost(r)) {
		return false
	}
	hdr := w.Header()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		hdr.Set("Allow", "GET, HEAD")
		refuse(w, r, report.ProxyMethodNotAllowed, http.StatusMethodNotAllowed)
		return true
	}
	body := asset.body(h)
	hdr.Set("Content-Type", asset.contentType)
	hdr.Set("Content-Length", strconv.Itoa(len(body)))
	// The worker and the manifest must update with the core.
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("X-Content-Type-Options", "nosniff")
	switch r.URL.Path {
	case "/sw.js":
		hdr.Set("Service-Worker-Allowed", "/")
	case "/offline.html":
		hdr.Set("Content-Security-Policy", homeCSP)
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(body)
	}
	return true
}

// markPNGs holds the mark at each size, drawn once.
var markPNGs = map[int]func() []byte{
	180: sync.OnceValue(func() []byte { return drawMark(180) }),
	192: sync.OnceValue(func() []byte { return drawMark(192) }),
	512: sync.OnceValue(func() []byte { return drawMark(512) }),
}

// markPNG is Nearhost's mark as a PNG of size by size pixels, for 180, 192
// and 512. Any other size is drawn each time.
func markPNG(size int) []byte {
	if f, ok := markPNGs[size]; ok {
		return f()
	}
	return drawMark(size)
}

var (
	markGround = color.NRGBA{0x0F, 0x12, 0x10, 0xFF}
	markArch   = color.NRGBA{0xD6, 0xF2, 0x6B, 0xFF}
	markDot    = color.NRGBA{0xEC, 0xEF, 0xE8, 0xFF}
)

// supersample is how many samples per side make one pixel.
const supersample = 4

// drawMark draws the mark on the 64-unit grid, scaled to size: the ground
// over the whole square, the arch M18 52V34a14 14 0 0 1 28 0v18 stroked 10
// wide with round caps, and the dot at (32,46), radius 5.5. Each pixel
// averages 4x4 samples.
func drawMark(size int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	scale := 64 / float64(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b float64
			for sy := 0; sy < supersample; sy++ {
				for sx := 0; sx < supersample; sx++ {
					px := (float64(x) + (float64(sx)+0.5)/supersample) * scale
					py := (float64(y) + (float64(sy)+0.5)/supersample) * scale
					c := markGround
					switch {
					case math.Hypot(px-32, py-46) <= 5.5:
						c = markDot
					case archDistance(px, py) <= 5:
						c = markArch
					}
					r += float64(c.R)
					g += float64(c.G)
					b += float64(c.B)
				}
			}
			n := float64(supersample * supersample)
			img.SetNRGBA(x, y, color.NRGBA{uint8(math.Round(r / n)), uint8(math.Round(g / n)), uint8(math.Round(b / n)), 0xFF})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err) // encoding to memory cannot fail
	}
	return buf.Bytes()
}

// archDistance is the distance from (x, y) to the arch's centre line: two
// stems from y=34 to y=52 at x=18 and x=46, joined by the half circle of
// radius 14 about (32,34).
func archDistance(x, y float64) float64 {
	d := math.Min(stemDistance(x, y, 18), stemDistance(x, y, 46))
	if y <= 34 {
		d = math.Min(d, math.Abs(math.Hypot(x-32, y-34)-14))
	}
	return d
}

// stemDistance is the distance from (x, y) to the vertical segment at sx
// from y=34 to y=52.
func stemDistance(x, y, sx float64) float64 {
	return math.Hypot(x-sx, y-math.Max(34, math.Min(52, y)))
}
