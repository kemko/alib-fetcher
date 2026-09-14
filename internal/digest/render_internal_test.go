package digest

import (
	"strings"
	"testing"

	"github.com/kemko/alib-fetcher/internal/alib"
)

func TestRenderedRuneCountCountsDisplayedText(t *testing.T) {
	t.Parallel()

	// Given
	richHTML := `<b>A &amp; Б</b><br/><a href="https://example.com/very-long">ссылка</a><hr/>Фото`

	// When
	count := renderedRuneCount(richHTML)

	// Then
	if count != len([]rune("A & Б\nссылкаФото")) {
		t.Fatalf("renderedRuneCount() = %d, want %d", count, len([]rune("A & Б\nссылкаФото")))
	}
}

func TestTruncateContentUsesRenderedHTMLByteLimit(t *testing.T) {
	t.Parallel()

	// Given
	book := alib.Book{
		Title:   "Книга",
		Content: strings.Repeat("<&🛸\n", 10000),
		BuyURL:  "https://example.com/book",
	}
	options := Options{Limit: 32000}

	// When
	rendered := truncateContent(book, options)
	contentRunes := []rune(strings.TrimSpace(book.Content))
	low, high := 0, len(contentRunes)
	for low < high {
		middle := (low + high + 1) / 2
		candidate := book
		candidate.Content = string(contentRunes[:middle]) + "…"
		if !exceedsTextLimits(renderBook(candidate, options), options.Limit-1) {
			low = middle
			continue
		}
		high = middle - 1
	}
	expected := book
	expected.Content = string(contentRunes[:low]) + "…"

	// Then
	if len(rendered) > richMessageHTMLByteLimit {
		t.Fatalf("len(rendered) = %d, want at most %d", len(rendered), richMessageHTMLByteLimit)
	}
	if !strings.Contains(rendered, "&lt;&amp;🛸<br/>") {
		t.Fatalf("rendered content lost escaped source runes: %q", rendered)
	}
	if rendered != renderBook(expected, options) {
		t.Fatalf("truncateContent() did not retain the longest fitting source-rune prefix")
	}
	if !strings.Contains(rendered, "…") {
		t.Fatalf("rendered content was not truncated: %q", rendered)
	}
}
