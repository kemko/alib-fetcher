package digest_test

import (
	"strings"
	"testing"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/digest"

	"github.com/stretchr/testify/require"
)

func Test_Render_escapes_HTML_and_keeps_chunks_within_limit(t *testing.T) {
	t.Parallel()

	// Given
	const messageLimit = 420
	books := []alib.Book{
		{
			Title:            "Автор. A < B и очень длинное название без обрезки.",
			TextBeforeSeller: "Описание & библиография.\n(Условия продавца",
			Seller:           "BS & Co",
			SellerURL:        "https://example.com/seller?a=1&b=2",
			TextBeforeBuy:    ") Цена: 100 руб.",
			BuyURL:           "https://example.com/1?a=1&b=2",
			TextAfterBuy:     "\nПолная аннотация.\nСостояние: Хорошее.",
			HasPhotos:        true,
		},
		{Title: strings.Repeat("Книга ", 20), BuyURL: "https://example.com/2"},
	}

	// When
	chunks, err := digest.Render(books, messageLimit)

	// Then
	require.NoError(t, err)
	require.Len(t, chunks, 2)
	require.Equal(t, `<b>Новые книги на Alib.ru</b>

<b>Автор. A &lt; B и очень длинное название без обрезки.</b> Описание &amp; библиография.
(Условия продавца <a href="https://example.com/seller?a=1&amp;b=2">BS &amp; Co</a>) Цена: 100 руб. <a href="https://example.com/1?a=1&amp;b=2">Купить</a>
Полная аннотация.
Состояние: Хорошее.
Фото: есть`, chunks[0].Text)
	require.NotContains(t, chunks[0].Text, "A < B")
	for _, chunk := range chunks {
		require.LessOrEqual(t, len([]rune(chunk.Text)), messageLimit)
		require.NotEmpty(t, chunk.Books)
	}
}

func Test_Render_reports_when_listing_has_no_photos(t *testing.T) {
	t.Parallel()

	// Given
	books := []alib.Book{{Title: "Книга", BuyURL: "https://example.com/1"}}

	// When
	chunks, err := digest.Render(books, 4096)

	// Then
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	require.Contains(t, chunks[0].Text, "Фото: нет")
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
