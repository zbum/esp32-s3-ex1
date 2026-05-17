// Package console renders a terminal-style text region onto an ST7789V3 TFT.
//
// Lines fill the screen top-down. Once the last visible row is occupied, each
// new line scrolls the previous content up by one cell using the panel's
// hardware vertical-scroll registers (VSCRDEF / VSCRSADD), so only a single
// character-row blit is sent over SPI per line.
//
// The package assumes a monospace tinyfont and a fixed line-cell height. It
// targets the 1.69" 240x280 panel by default (RowOffset 20), but the geometry
// is configurable for other ST7789V3 panels.
package console

import (
	"image/color"

	"tinygo.org/x/drivers/st7789"
	"tinygo.org/x/tinyfont"
)

// Config captures the display geometry and the text-cell metrics derived from
// the chosen font. All units are pixels.
//
// Coordinates are in display-area space (0..Width, 0..Height). Any physical
// rowstart/columnstart belongs in the st7789.Config, not here: the underlying
// driver folds those into setWindow already, and SetScroll inside this
// package follows the same convention so the math stays in [0, Height).
//
// Font assumptions: the package writes one monospace glyph per character cell
// (Width / CharAdvance columns × Height / LineHeight rows). Proportional
// fonts will overflow rows and corrupt the ring; pass a monospace tinyfont.
type Config struct {
	Width       int16 // visible width
	Height      int16 // visible height
	LineHeight  int16 // pixel height of one line cell (must be > 0)
	Baseline    int16 // baseline Y within a line cell (distance from cell top)
	CharAdvance int16 // pixels per glyph (monospace x-advance, must be > 0)
	Foreground  color.RGBA
	Background  color.RGBA
}

// Console is a scrolling text region driven by an ST7789-family display.
type Console struct {
	disp   *st7789.Device
	font   tinyfont.Fonter
	cfg    Config
	vsaH   int16 // height of the vertical scroll area = rows * LineHeight
	rows   int16
	cols   int16
	nextLn uint32 // monotonic count of lines written since Init
}

// New builds a Console. The caller must Configure the ST7789 device first.
// Init must be called before the first write. Returns nil if Config has
// zero LineHeight, CharAdvance, Width, or Height — these would either
// divide-by-zero or produce a zero-row console.
func New(disp *st7789.Device, font tinyfont.Fonter, cfg Config) *Console {
	if cfg.LineHeight <= 0 || cfg.CharAdvance <= 0 || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil
	}
	rows := cfg.Height / cfg.LineHeight
	if rows <= 0 {
		return nil
	}
	return &Console{
		disp: disp,
		font: font,
		cfg:  cfg,
		rows: rows,
		vsaH: rows * cfg.LineHeight,
		cols: cfg.Width / cfg.CharAdvance,
	}
}

// Init installs the hardware scroll region and clears the screen. The bottom
// (Height - rows*LineHeight) pixels are reserved as a fixed background strip
// so each line stays cell-aligned and the scroll address never straddles a
// glyph row.
func (c *Console) Init() {
	c.disp.SetScrollArea(0, c.cfg.Height-c.vsaH)
	c.Clear()
}

// Clear blanks the screen and resets the cursor to the first row.
func (c *Console) Clear() {
	c.disp.FillScreen(c.cfg.Background)
	c.disp.SetScroll(0)
	c.nextLn = 0
}

// Cols returns the number of full character columns that fit on screen.
func (c *Console) Cols() int { return int(c.cols) }

// Rows returns the number of full character rows that fit on screen.
func (c *Console) Rows() int { return int(c.rows) }

// Println writes a single logical line and advances the cursor. Lines wider
// than Cols() are wrapped onto consecutive rows.
func (c *Console) Println(s string) {
	if s == "" {
		c.writeLine("")
		return
	}
	runes := []rune(s)
	for len(runes) > 0 {
		chunk := runes
		if int16(len(chunk)) > c.cols {
			chunk = runes[:c.cols]
			runes = runes[c.cols:]
		} else {
			runes = nil
		}
		c.writeLine(string(chunk))
	}
}

// Print writes text, treating any embedded '\n' as a line terminator. A '\r'
// immediately preceding a '\n' is stripped so CRLF streams render cleanly.
// The final segment (if it lacks a trailing newline) is committed as its own
// line — this is a line-oriented console, not a character-oriented one.
func (c *Console) Print(s string) {
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '\n' {
			continue
		}
		end := i
		if end > start && s[end-1] == '\r' {
			end--
		}
		c.Println(s[start:end])
		start = i + 1
	}
	if start < len(s) {
		c.Println(s[start:])
	}
}

// Write implements an io.Writer-shaped API so callers can feed buffered byte
// streams (e.g. log output). It splits on '\n' the same way Print does.
func (c *Console) Write(p []byte) (int, error) {
	c.Print(string(p))
	return len(p), nil
}

// writeLine commits exactly one cell-aligned row. The hardware scroll register
// is updated *before* the blit so the new row appears at the visible bottom
// without first flashing at the top.
//
// SetScroll's argument is passed straight through to VSCRSADD by the driver
// (in Rotation0). Keeping the value in display-area coordinates [0, vsaH)
// — i.e. without the panel rowstart — matches what setWindow does when the
// console writes pixels via FillRectangle / tinyfont.WriteLine, so content
// and scroll address stay in lockstep.
func (c *Console) writeLine(text string) {
	rows := uint32(c.rows)
	if c.nextLn >= rows {
		topRing := (c.nextLn - rows + 1) % rows
		c.disp.SetScroll(int16(int32(topRing) * int32(c.cfg.LineHeight)))
	}
	ringPos := c.nextLn % rows
	ramY := int16(int32(ringPos) * int32(c.cfg.LineHeight))
	c.disp.FillRectangle(0, ramY, c.cfg.Width, c.cfg.LineHeight, c.cfg.Background)
	if text != "" {
		tinyfont.WriteLine(c.disp, c.font, 0, ramY+c.cfg.Baseline, text, c.cfg.Foreground)
	}
	c.nextLn++
}
