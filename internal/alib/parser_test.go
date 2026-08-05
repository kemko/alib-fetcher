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
<p><b>Мартынов Г. Каллистяне.</b> Научно-фантастический роман. Москва Детгиз 1960 г.<br>
(До заказа внимательно прочтите условия продажи продавца <a href="/bs.php4?bs=test">BS - test</a>, Москва.)
Цена: 1 200 руб. <a href="/book-1.html"><b>Купить</b></a><br>
Вторая книга романа Каллисто.<br>Состояние: Отличное.<br>
Смотрите: <a href="/foto.php4?id=1">фото</a> - <a href="/foto.php4?id=2">фото</a></p>
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
			Title: "Мартынов Г. Каллистяне.",
			TextBeforeSeller: "Научно-фантастический роман. Москва Детгиз 1960 г.\n" +
				"(До заказа внимательно прочтите условия продажи продавца",
			Seller:        "BS - test",
			SellerURL:     "https://www.alib.ru/bs.php4?bs=test",
			TextBeforeBuy: ", Москва.) Цена: 1 200 руб.",
			BuyURL:        "https://www.alib.ru/book-1.html",
			TextAfterBuy:  "\nВторая книга романа Каллисто.\nСостояние: Отличное.",
			HasPhotos:     true,
		},
		{
			Title:            "Вторая книга.",
			TextBeforeSeller: "Цена: 500 руб.",
			BuyURL:           "https://www.alib.ru/book-2.html",
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
