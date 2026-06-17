package jobreport

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// renderTable draws a Unicode box table. rows[0] is the header (centered);
// remaining rows are left-aligned. Cells longer than maxw are truncated with "...".
// Returns the table as a string (trailing newline included).
func renderTable(rows [][]string, maxw int) string {
	if len(rows) == 0 {
		return ""
	}
	ncol := 0
	for _, r := range rows {
		if len(r) > ncol {
			ncol = len(r)
		}
	}
	// normalize + truncate, track column widths (by rune count)
	cells := make([][]string, len(rows))
	width := make([]int, ncol)
	for i, r := range rows {
		cells[i] = make([]string, ncol)
		for j := 0; j < ncol; j++ {
			v := ""
			if j < len(r) {
				v = r[j]
			}
			if utf8.RuneCountInString(v) > maxw {
				v = truncRunes(v, maxw-3) + "..."
			}
			cells[i][j] = v
			if w := utf8.RuneCountInString(v); w > width[j] {
				width[j] = w
			}
		}
	}
	rule := func(l, mid, r string) string {
		var b strings.Builder
		b.WriteString(l)
		for j := 0; j < ncol; j++ {
			b.WriteString(strings.Repeat("─", width[j]+2))
			if j < ncol-1 {
				b.WriteString(mid)
			}
		}
		b.WriteString(r)
		b.WriteByte('\n')
		return b.String()
	}
	var b strings.Builder
	b.WriteString(rule("┌", "┬", "┐"))
	for i, r := range cells {
		b.WriteString("│")
		for j := 0; j < ncol; j++ {
			pad := width[j] - utf8.RuneCountInString(r[j])
			if i == 0 { // header: centered
				lp := pad / 2
				rp := pad - lp
				b.WriteString(" " + strings.Repeat(" ", lp) + r[j] + strings.Repeat(" ", rp) + " │")
			} else { // body: left-aligned
				b.WriteString(" " + r[j] + strings.Repeat(" ", pad) + " │")
			}
		}
		b.WriteByte('\n')
		switch {
		case i == 0:
			b.WriteString(rule("├", "┼", "┤"))
		case i < len(cells)-1:
			b.WriteString(rule("├", "┼", "┤"))
		default:
			b.WriteString(rule("└", "┴", "┘"))
		}
	}
	return b.String()
}

func truncRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	cnt := 0
	for i := range s {
		if cnt == n {
			return s[:i]
		}
		cnt++
	}
	return s
}

// num formats a float like jq's `round*K/K` then %g: trims trailing zeros.
// dec is the number of decimal places to round to.
func num(v float64, dec int) string {
	pow := math.Pow(10, float64(dec))
	r := math.Round(v*pow) / pow
	return strconv.FormatFloat(r, 'f', -1, 64)
}
