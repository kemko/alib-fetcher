package alib_test

import (
	"bytes"
	"net/url"
	"testing"

	"github.com/kemmko/alib-fetcher/internal/alib"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding/charmap"
)

func Test_Parse_returns_unique_books_from_windows1251_page(t *testing.T) {
	t.Parallel()

	// Given
	page := `<html><head><meta charset="windows-1251"></head><body>
<p><b>Книга &amp; автор.</b> Описание.<br>
(До заказа прочтите условия продавца <a href="/bs.php4?bs=test">BS - test</a>, Москва.)
Цена: 1 200 руб. <a href="/book-1.html"><b>Купить</b></a><br>Состояние: Отличное</p>
<p><b>Дубликат.</b> Цена: 999 руб. <a href="/book-1.html"><b>Купить</b></a></p>
<p><b>Вторая книга.</b> Цена: 500 руб. <a href="https://www.alib.ru/book-2.html"><b>Купить</b></a></p>
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
			Title:     "Книга & автор.",
			Seller:    "BS - test",
			Price:     "1 200 руб.",
			Condition: "Отличное",
			BuyURL:    "https://www.alib.ru/book-1.html",
		},
		{
			Title:  "Вторая книга.",
			Price:  "500 руб.",
			BuyURL: "https://www.alib.ru/book-2.html",
		},
	}, books)
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
