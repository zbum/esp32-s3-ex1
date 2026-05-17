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
//
// IMPORTANT: the underlying tinygo.org/x/drivers/st7789 driver zeroes the
// panel row offset at Rotation0. To support panels whose visible window does
// not start at GRAM row 0 (e.g. the Waveshare 1.69" panel, where the visible
// area is GRAM rows 20..299), configure the driver with the controller's
// full GRAM dimensions (Width=240, Height=GRAMHeight=320) and tell the
// console where the visible window lives via Config.RowOffset/ColOffset.
// The console then folds those offsets into every setWindow call and into
// the hardware scroll address so content and scroll stay in lockstep.
package console

import (
	"image/color"

	"tinygo.org/x/drivers/st7789"
	"tinygo.org/x/tinyfont"
)

// GRAMHeight is the ST7789 controller's frame-memory height in rows. The
// panel may show only a subset of these rows; the rest are off-screen and
// where the scroll-area bottom-fixed-area absorbs any padding.
const GRAMHeight = 320

// Config captures the visible panel geometry, the panel's location inside
// the ST7789 GRAM, and the text-cell metrics derived from the chosen font.
// All units are pixels.
//
// Width / Height describe the panel's *visible* area. RowOffset / ColOffset
// describe where that visible area begins inside the controller's 240x320
// GRAM:
//
//   - 2.0" / 1.9" 240x320 panel: Width=240 Height=320 RowOffset=0 ColOffset=0
//   - 1.69" 240x280 panel:       Width=240 Height=280 RowOffset=20 ColOffset=0
//   - 1.47" 172x320 panel:       Width=172 Height=320 RowOffset=0  ColOffset=34
//
// The companion st7789.Device must be Configure'd with the full GRAM
// dimensions (Width=240, Height=GRAMHeight) and zero rowstart/colstart —
// this package manages the offset itself because the driver does not at
// Rotation0.
//
// Font assumptions: the package writes one monospace glyph per character cell
// (Width / CharAdvance columns × Height / LineHeight rows). Proportional
// fonts will overflow rows and corrupt the ring; pass a monospace tinyfont.
type Config struct {
	Width       int16 // visible width
	Height      int16 // visible height
	RowOffset   int16 // panel rowstart in the ST7789 GRAM (0 if the panel covers GRAM row 0)
	ColOffset   int16 // panel colstart in the ST7789 GRAM (0 for most panels)
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

// New builds a Console. The caller must Configure the ST7789 device first
// with the full controller GRAM dimensions (typically Width=240, Height=320).
// Init must be called before the first write. Returns nil if Config has
// zero LineHeight, CharAdvance, Width, or Height — these would either
// divide-by-zero or produce a zero-row console.
func New(disp *st7789.Device, font tinyfont.Fonter, cfg Config) *Console {
	if cfg.LineHeight <= 0 || cfg.CharAdvance <= 0 || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil
	}
	if cfg.RowOffset < 0 || cfg.ColOffset < 0 || cfg.RowOffset+cfg.Height > GRAMHeight {
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

// Init installs the hardware scroll region and clears the screen. The
// vertical-scroll area covers GRAM rows [RowOffset, RowOffset + rows*LineHeight),
// so any cell-aligned scroll value lands inside it without straddling the
// VSA boundary.
func (c *Console) Init() {
	topFixed := c.cfg.RowOffset
	bottomFixed := GRAMHeight - topFixed - c.vsaH
	if bottomFixed < 0 {
		bottomFixed = 0
	}
	c.disp.SetScrollArea(topFixed, bottomFixed)
	c.Clear()
}

// Clear blanks the screen and resets the cursor to the first row. The fill
// covers the visible window only — off-screen GRAM rows are left alone.
func (c *Console) Clear() {
	if c.cfg.Height > 0 {
		c.disp.FillRectangle(c.cfg.ColOffset, c.cfg.RowOffset, c.cfg.Width, c.cfg.Height, c.cfg.Background)
	}
	c.disp.SetScroll(c.cfg.RowOffset)
	c.nextLn = 0
}

// Cols returns the number of full character columns that fit on screen.
func (c *Console) Cols() int { return int(c.cols) }

// Rows returns the number of full character rows that fit on screen.
func (c *Console) Rows() int { return int(c.rows) }

// Println writes a single logical line and advances the cursor. Lines wider
// than Cols() are wrapped onto consecutive rows.
func (c *Console) Println(s string) {
	c.PrintlnColor(s, c.cfg.Foreground)
}

// PrintlnColor writes a single logical line in the supplied foreground color
// instead of the default. The background and cell metrics are unchanged so a
// colored line still fits the ring and scrolls like any other line — useful
// for status accents (success in green, errors in red, etc.).
func (c *Console) PrintlnColor(s string, fg color.RGBA) {
	if s == "" {
		c.writeLine("", fg)
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
		c.writeLine(string(chunk), fg)
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
// Coordinates are in raw GRAM space — the driver does not add a row offset at
// Rotation0, so the console adds RowOffset itself for both the blit position
// and the scroll address. That keeps content and scroll register in lockstep
// regardless of which panel variant is wired up.
func (c *Console) writeLine(text string, fg color.RGBA) {
	rows := uint32(c.rows)
	if c.nextLn >= rows {
		topRing := (c.nextLn - rows + 1) % rows
		c.disp.SetScroll(int16(int32(c.cfg.RowOffset) + int32(topRing)*int32(c.cfg.LineHeight)))
	}
	ringPos := c.nextLn % rows
	ramY := int16(int32(c.cfg.RowOffset) + int32(ringPos)*int32(c.cfg.LineHeight))
	c.disp.FillRectangle(c.cfg.ColOffset, ramY, c.cfg.Width, c.cfg.LineHeight, c.cfg.Background)
	if text != "" {
		tinyfont.WriteLine(c.disp, c.font, c.cfg.ColOffset, ramY+c.cfg.Baseline, text, fg)
	}
	c.nextLn++
}
