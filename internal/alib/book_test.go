package alib_test

import (
	"encoding/json"
	"testing"

	"github.com/kemko/alib-fetcher/internal/alib"

	"github.com/stretchr/testify/require"
)

func Test_Book_marshals_semantic_JSON_schema(t *testing.T) {
	t.Parallel()

	// Given
	book := semanticBook("https://www.alib.ru/book.html")

	// When
	encoded, err := json.Marshal(book)

	// Then
	require.NoError(t, err)
	require.JSONEq(t, `{
		"title": "Мартынов Г. Каллистяне.",
		"bibliography": "Научно-фантастический роман. М., 2026г.",
		"content": "Вторая книга романа Каллисто.",
		"seller": "BotSad",
		"seller_url": "https://www.alib.ru/bs.php4?bs=BotSad",
		"location": "Москва",
		"price": "3 900 руб.",
		"condition": "Состояние: Отличное.\nКомплект полный.",
		"buy_url": "https://www.alib.ru/book.html",
		"publication_year": 2026,
		"has_photos": true
	}`, string(encoded))
	require.NotContains(t, string(encoded), "text_before_seller")
	require.NotContains(t, string(encoded), "text_before_buy")
	require.NotContains(t, string(encoded), "text_after_buy")
}

func Test_Book_unmarshals_legacy_fragments_into_semantic_fields(t *testing.T) {
	t.Parallel()

	// Given
	legacy := []byte(`{
		"title": "Мартынов Г. Каллистяне.",
		"text_before_seller": "Научно-фантастический роман. М., 2026г.\n(До заказа внимательно прочтите условия продажи продавца",
		"seller": "BS - BotSad",
		"seller_url": "https://www.alib.ru/bs.php4?bs=BotSad",
		"text_before_buy": ", Москва.) Цена: 3 900 руб.",
		"buy_url": "https://www.alib.ru/book.html",
		"text_after_buy": "\nВторая книга романа Каллисто.\nСостояние: Отличное.\nКомплект полный.",
		"has_photos": true
	}`)

	// When
	var book alib.Book
	err := json.Unmarshal(legacy, &book)

	// Then
	require.NoError(t, err)
	expected := semanticBook("https://www.alib.ru/book.html")
	require.Equal(t, expected, book)
}

func Test_Book_unmarshals_legacy_bibliography_with_same_line_seller_preamble(t *testing.T) {
	t.Parallel()

	// Given
	legacy := []byte(`{
		"title": "Книга.",
		"text_before_seller": "М., 2026 г. (До заказа внимательно прочтите условия продажи продавца",
		"seller": "BS - Seller",
		"seller_url": "https://www.alib.ru/bs.php4?bs=Seller",
		"text_before_buy": ", Москва.) Цена: 100 руб.",
		"buy_url": "https://www.alib.ru/book.html",
		"text_after_buy": "",
		"has_photos": false
	}`)

	// When
	var book alib.Book
	err := json.Unmarshal(legacy, &book)

	// Then
	require.NoError(t, err)
	require.Equal(t, "М., 2026 г.", book.Bibliography)
	require.Equal(t, 2026, book.PublicationYear)
	require.Equal(t, "Seller", book.Seller)
	require.Equal(t, "Москва", book.Location)
	require.Equal(t, "100 руб.", book.Price)
}

func Test_Book_unmarshals_legacy_listing_without_seller(t *testing.T) {
	t.Parallel()

	// Given
	legacy := []byte(`{
		"title": "Книга без продавца.",
		"text_before_seller": "Л., 1970 г. ISBN 5-0000-1970-2.\nЦена: 500 руб.",
		"seller": "",
		"seller_url": "",
		"text_before_buy": "",
		"buy_url": "https://www.alib.ru/book-without-seller.html",
		"text_after_buy": "\nСостояние: Хорошее.",
		"has_photos": false
	}`)

	// When
	var book alib.Book
	err := json.Unmarshal(legacy, &book)

	// Then
	require.NoError(t, err)
	require.Equal(t, "Книга без продавца.", book.Title)
	require.Equal(t, "Л., 1970 г. ISBN 5-0000-1970-2.", book.Bibliography)
	require.Equal(t, 1970, book.PublicationYear)
	require.Empty(t, book.Content)
	require.Empty(t, book.Seller)
	require.Empty(t, book.SellerURL)
	require.Empty(t, book.Location)
	require.Equal(t, "500 руб.", book.Price)
	require.Equal(t, "Состояние: Хорошее.", book.Condition)
	require.Equal(t, "https://www.alib.ru/book-without-seller.html", book.BuyURL)
	require.False(t, book.HasPhotos)
}

func semanticBook(buyURL string) alib.Book {
	return alib.Book{
		Title:           "Мартынов Г. Каллистяне.",
		Bibliography:    "Научно-фантастический роман. М., 2026г.",
		PublicationYear: 2026,
		Content:         "Вторая книга романа Каллисто.",
		Seller:          "BotSad",
		SellerURL:       "https://www.alib.ru/bs.php4?bs=BotSad",
		Location:        "Москва",
		Price:           "3 900 руб.",
		Condition:       "Состояние: Отличное.\nКомплект полный.",
		BuyURL:          buyURL,
		HasPhotos:       true,
	}
}
