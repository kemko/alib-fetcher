package alib_test

import (
	"bytes"
	"net/url"
	"testing"

	"github.com/kemko/alib-fetcher/internal/alib"

	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding/charmap"
)

func Test_Parse_returns_semantic_unique_books_from_windows1251_page(t *testing.T) {
	t.Parallel()

	// Given
	page := `<html><head><meta charset="windows-1251"></head><body>
<p><b>Мартынов Г. Каллистяне.</b> Научно-фантастический роман. ISBN 978-5-0000-2025-1. М., 2026г.<br>
(До заказа внимательно прочтите условия продажи продавца <a href="/bs.php4?bs=BotSad">BS - BotSad</a>, Москва.)
Цена: 3 900 руб. <a href="/book-1.html"><b>Купить</b></a><br>
Первая строка содержания.<br>Переиздание текста 1970 г.<br>
Состояние: Отличное.<br>Комплект полный.<br>
Смотрите: <a href="/foto.php4?id=1">фото</a> - <a href="/foto.php4?id=2">фото</a></p>
<p><b>Книга без содержания.</b> Л., 1970 г. ISBN 5-0000-1970-2.<br>
Цена: 500 руб. <a href="https://www.alib.ru/book-2.html"><b>Купить</b></a><br>
Состояние: Хорошее.</p>
<p><b>Дубликат.</b> Цена: 999 руб. <a href="/book-1.html"><b>Купить</b></a></p>
</body></html>`
	encoded, err := charmap.Windows1251.NewEncoder().Bytes([]byte(page))
	require.NoError(t, err)
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	books, err := alib.Parse(bytes.NewReader(encoded), baseURL, "text/html; charset=windows-1251")

	// Then
	require.NoError(t, err)
	require.Equal(t, []alib.Book{
		{
			Title:           "Мартынов Г. Каллистяне.",
			Bibliography:    "Научно-фантастический роман. ISBN 978-5-0000-2025-1. М., 2026г.",
			PublicationYear: 2026,
			Content:         "Первая строка содержания.\nПереиздание текста 1970 г.",
			Seller:          "BotSad",
			SellerURL:       "https://www.alib.ru/bs.php4?bs=BotSad",
			Location:        "Москва",
			Price:           "3 900 руб.",
			Condition:       "Состояние: Отличное.\nКомплект полный.",
			BuyURL:          "https://www.alib.ru/book-1.html",
			HasPhotos:       true,
		},
		{
			Title:           "Книга без содержания.",
			Bibliography:    "Л., 1970 г. ISBN 5-0000-1970-2.",
			PublicationYear: 1970,
			Price:           "500 руб.",
			Condition:       "Состояние: Хорошее.",
			BuyURL:          "https://www.alib.ru/book-2.html",
		},
	}, books)
}

func Test_Parse_extracts_last_bibliographic_year_and_ignores_content_year(t *testing.T) {
	t.Parallel()

	// Given
	page := `<p><b>Книга.</b> Первое издание 1970 г., второе издание 2026г. ISBN 5-2025-0000-1.<br>
Цена: 100 руб. <a href="/book.html"><b>Купить</b></a><br>
События происходят в 2030 г.</p>`
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	books, err := alib.Parse(bytes.NewBufferString(page), baseURL, "text/html")

	// Then
	require.NoError(t, err)
	require.Len(t, books, 1)
	require.Equal(t, 2026, books[0].PublicationYear)
	require.Equal(t, "События происходят в 2030 г.", books[0].Content)
}

func Test_Parse_does_not_extract_year_outside_bibliography(t *testing.T) {
	t.Parallel()

	// Given
	page := `<p><b>Книга.</b> ISBN 978-5-2026-0000-1.<br>
Цена: 100 руб. <a href="/book.html"><b>Купить</b></a><br>
Издание 2025 г.</p>`
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	books, err := alib.Parse(bytes.NewBufferString(page), baseURL, "text/html")

	// Then
	require.NoError(t, err)
	require.Len(t, books, 1)
	require.Zero(t, books[0].PublicationYear)
}

func Test_Parse_rejects_page_without_books(t *testing.T) {
	t.Parallel()

	// Given
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	books, err := alib.Parse(bytes.NewBufferString("<html><body>empty</body></html>"), baseURL, "text/html")

	// Then
	require.ErrorIs(t, err, alib.ErrNoBooks)
	require.Empty(t, books)
}
