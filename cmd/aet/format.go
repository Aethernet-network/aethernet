package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// printHeader prints a titled section with a separator line underneath.
func printHeader(title string) {
	fmt.Println(title)
	fmt.Println(strings.Repeat("─", 21))
}

// printRow prints a label-value pair with a fixed-width label column.
func printRow(label, value string) {
	fmt.Printf("  %-20s %s\n", label+":", value)
}

// printTable prints a formatted table with headers and rows, computing column
// widths automatically to fit the widest cell in each column.
func printTable(headers []string, rows [][]string) {
	if len(rows) == 0 {
		fmt.Println("  (none)")
		return
	}

	cols := len(headers)
	widths := make([]int, cols)
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i := 0; i < cols && i < len(row); i++ {
			if len(row[i]) > widths[i] {
				widths[i] = len(row[i])
			}
		}
	}

	// Header row.
	fmt.Print("  ")
	for i, h := range headers {
		fmt.Printf("%-*s", widths[i]+2, h)
	}
	fmt.Println()

	// Separator row.
	fmt.Print("  ")
	for _, w := range widths {
		fmt.Print(strings.Repeat("─", w) + "  ")
	}
	fmt.Println()

	// Data rows.
	for _, row := range rows {
		fmt.Print("  ")
		for i := 0; i < cols; i++ {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			fmt.Printf("%-*s", widths[i]+2, cell)
		}
		fmt.Println()
	}
}

// formatAET formats a micro-AET amount as a human-readable string like "1,234 AET".
func formatAET(microAET uint64) string {
	return formatNumber(microAET) + " AET"
}

// formatNumber inserts thousands separators into n.
func formatNumber(n uint64) string {
	s := strconv.FormatUint(n, 10)
	if len(s) <= 3 {
		return s
	}
	result := make([]byte, 0, len(s)+(len(s)-1)/3)
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, s[i])
	}
	return string(result)
}

// formatTimeAgo formats a Unix timestamp as a relative time string like
// "2 hours ago" or "3 days ago". Returns "never" for a zero timestamp.
func formatTimeAgo(unix int64) string {
	if unix == 0 {
		return "never"
	}
	diff := time.Now().Unix() - unix
	if diff < 0 {
		diff = 0
	}
	switch {
	case diff < 60:
		return "just now"
	case diff < 3600:
		m := diff / 60
		if m == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", m)
	case diff < 86400:
		h := diff / 3600
		if h == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	case diff < 86400*30:
		d := diff / 86400
		if d == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", d)
	default:
		mo := diff / (86400 * 30)
		if mo == 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", mo)
	}
}

// truncateID shortens a hash or ID for display, e.g. "a3f8b2c9...f1a2b3c4".
// Returns the full id when len(id) <= maxLen.
func truncateID(id string, maxLen int) string {
	if maxLen < 8 || len(id) <= maxLen {
		return id
	}
	half := (maxLen - 3) / 2
	return id[:half] + "..." + id[len(id)-half:]
}
