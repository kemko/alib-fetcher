package digest

import (
	"strings"
	"testing"
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

func TestChunkExceedsLimitsIncludesHTMLBytes(t *testing.T) {
	t.Parallel()

	if chunkExceedsLimits(1, strings.Repeat("a", richMessageHTMLByteLimit), 40000) {
		t.Fatal("chunk at HTML byte limit exceeds limits")
	}
	if !chunkExceedsLimits(1, strings.Repeat("a", richMessageHTMLByteLimit+1), 40000) {
		t.Fatal("chunk above HTML byte limit does not exceed limits")
	}
}
