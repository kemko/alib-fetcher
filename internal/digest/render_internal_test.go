package digest

import "testing"

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
