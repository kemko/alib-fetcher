package digest_test

import (
	"strings"
	"testing"

	"github.com/kemmko/alib-fetcher/internal/alib"
	"github.com/kemmko/alib-fetcher/internal/digest"

	"github.com/stretchr/testify/require"
)

func Test_Render_escapes_HTML_and_keeps_chunks_within_limit(t *testing.T) {
	t.Parallel()

	// Given
	books := []alib.Book{
		{Title: "A < B", Seller: "BS & Co", Price: "100 руб.", Condition: "Хорошее", BuyURL: "https://example.com/1?a=1&b=2"},
		{Title: strings.Repeat("Книга ", 20), BuyURL: "https://example.com/2"},
	}

	// When
	chunks, err := digest.Render(books, 180)

	// Then
	require.NoError(t, err)
	require.Len(t, chunks, 2)
	require.Contains(t, chunks[0].Text, "A &lt; B")
	require.Contains(t, chunks[0].Text, "BS &amp; Co")
	require.NotContains(t, chunks[0].Text, "A < B")
	for _, chunk := range chunks {
		require.LessOrEqual(t, len([]rune(chunk.Text)), 180)
		require.NotEmpty(t, chunk.Books)
	}
}

func Test_Render_rejects_too_small_limit(t *testing.T) {
	t.Parallel()

	// Given
	books := []alib.Book{{Title: "Книга", BuyURL: "https://example.com/1"}}

	// When
	chunks, err := digest.Render(books, 10)

	// Then
	require.ErrorIs(t, err, digest.ErrMessageTooLong)
	require.Empty(t, chunks)
}
