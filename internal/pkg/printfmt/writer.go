// Package printfmt renders thermal printer output shared by the server (receipts, print queue) and
// the LAN print agent (offline tickets, decision W18): ESC/POS for 58/80 mm paper and TSPL labels.
// Text is plain ASCII; other characters print as '?'.
package printfmt

import (
	"bytes"
	"errors"
	"strings"
)

var ErrUnsupportedPaperWidth = errors.New("paper width must be 58 or 80 mm")

// ESC/POS control sequences.
var (
	Init      = []byte{0x1b, '@'}
	AlignLeft = []byte{0x1b, 'a', 0}
	AlignMid  = []byte{0x1b, 'a', 1}
	BoldOn    = []byte{0x1b, 'E', 1}
	BoldOff   = []byte{0x1b, 'E', 0}
	DoubleOn  = []byte{0x1d, '!', 0x11}
	DoubleOff = []byte{0x1d, '!', 0}
	FeedCut   = []byte{0x1d, 'V', 66, 3} // feed 3 lines, then partial cut
)

// Columns returns the characters per line for a paper width, or 0 when unsupported.
func Columns(widthMM int) int { return map[int]int{58: 32, 80: 48}[widthMM] }

// Writer builds an ESC/POS byte stream for a fixed number of columns.
type Writer struct {
	buf  bytes.Buffer
	cols int
}

func NewWriter(cols int) *Writer { return &Writer{cols: cols} }

func (p *Writer) Bytes() []byte { return p.buf.Bytes() }

func (p *Writer) Raw(b []byte) { p.buf.Write(b) }

func (p *Writer) Line(s string) {
	p.buf.WriteString(ASCII(s))
	p.buf.WriteByte('\n')
}

func (p *Writer) Sep() { p.Line(strings.Repeat("-", p.cols)) }

// Pair prints left and right on one line, the right part flush right; a left part that does not fit
// goes on its own line first.
func (p *Writer) Pair(left, right string) {
	left, right = ASCII(left), ASCII(right)
	if len(left)+1+len(right) > p.cols {
		p.Wrap(left, "")
		left = ""
	}
	p.Line(left + strings.Repeat(" ", p.cols-len(left)-len(right)) + right)
}

// Wrap breaks s into lines of at most cols characters at spaces, prefixing continuation lines with indent.
func (p *Writer) Wrap(s, indent string) {
	s = ASCII(strings.TrimSpace(s))
	if s == "" {
		return
	}
	cur := indent
	for _, word := range strings.Fields(s) {
		for len(indent)+len(word) > p.cols { // a single word longer than the line is hard-split
			if strings.TrimSpace(cur) != "" {
				p.Line(cur)
				cur = indent
			}
			cut := p.cols - len(indent)
			p.Line(indent + word[:cut])
			word = word[cut:]
		}
		switch {
		case strings.TrimSpace(cur) == "":
			cur = indent + word
		case len(cur)+1+len(word) <= p.cols:
			cur += " " + word
		default:
			p.Line(cur)
			cur = indent + word
		}
	}
	if strings.TrimSpace(cur) != "" {
		p.Line(cur)
	}
}

// QR prints a QR code (model 2, module size 6, error correction M) with GS ( k.
func (p *Writer) QR(data string) {
	d := []byte(data)
	n := len(d) + 3
	p.Raw([]byte{0x1d, '(', 'k', 4, 0, 49, 65, 50, 0})
	p.Raw([]byte{0x1d, '(', 'k', 3, 0, 49, 67, 6})
	p.Raw([]byte{0x1d, '(', 'k', 3, 0, 49, 69, 49})
	p.Raw([]byte{0x1d, '(', 'k', byte(n % 256), byte(n / 256), 49, 80, 48})
	p.Raw(d)
	p.Raw([]byte{0x1d, '(', 'k', 3, 0, 49, 81, 48})
	p.buf.WriteByte('\n')
}

// ASCII replaces every non-printable or non-ASCII character with '?'.
func ASCII(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r > 126 {
			return '?'
		}
		return r
	}, s)
}
