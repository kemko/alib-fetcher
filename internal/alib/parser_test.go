package alib_test

import (
	"bytes"
	"net/url"
	"os"
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
Смотрите: <a href="/foto.php4?id=1">фото</a> - <a href="foto.php4?id=2">фото</a> - <a href="/foto.php4?id=1">фото</a></p>
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
			Photos: []alib.Photo{
				{URL: "https://www.alib.ru/foto.php4?id=1", Caption: "фото"},
				{URL: "https://www.alib.ru/foto.php4?id=2", Caption: "фото"},
				{URL: "https://www.alib.ru/foto.php4?id=1", Caption: "фото"},
			},
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
	require.Empty(t, books[1].Photos)
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

func Test_Parse_preserves_photo_captions_order_and_repeats(t *testing.T) {
	t.Parallel()

	// Given
	page := `<p><b>Книга.</b> М., 2026 г.<br>
	Цена: 100 руб. <a href="/book.html"><b>Купить</b></a><br>
	Смотрите: <a href="/foto.php4?id=1"> Обложка </a> - <a href="foto.php4?id=2"><span> </span></a> -
	<a href="/foto.php4?id=1">Обложка</a></p>`
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	books, err := alib.Parse(bytes.NewBufferString(page), baseURL, "text/html")

	// Then
	require.NoError(t, err)
	require.Equal(t, []alib.Photo{
		{URL: "https://www.alib.ru/foto.php4?id=1", Caption: "Обложка"},
		{URL: "https://www.alib.ru/foto.php4?id=2", Caption: "фото"},
		{URL: "https://www.alib.ru/foto.php4?id=1", Caption: "Обложка"},
	}, books[0].Photos)
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

func Test_Parse_excludes_anchor_only_photo_section(t *testing.T) {
	t.Parallel()

	// Given
	page := `<p><b>Книга.</b> М., 2026 г.<br>
Цена: 100 руб. <a href="/book.html"><b>Купить</b></a><br>
Описание.<br>Состояние: Хорошее.<br>
<a href="/foto.php4?id=1">фото</a><br>Текст после фото.</p>`
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	books, err := alib.Parse(bytes.NewBufferString(page), baseURL, "text/html")

	// Then
	require.NoError(t, err)
	require.Len(t, books, 1)
	require.Equal(t, "Описание.", books[0].Content)
	require.Equal(t, "Состояние: Хорошее.", books[0].Condition)
	require.Equal(t, []alib.Photo{{URL: "https://www.alib.ru/foto.php4?id=1", Caption: "фото"}}, books[0].Photos)
}

func Test_Parse_extracts_bibliography_when_seller_is_on_title_line(t *testing.T) {
	t.Parallel()

	// Given
	page := `<p><b>Книга.</b> М., 2026 г. (До заказа внимательно прочтите условия продажи продавца
<a href="/bs.php4?bs=Seller">BS - Seller</a>, Москва.) Цена: 100 руб.
<a href="/book.html"><b>Купить</b></a></p>`
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	books, err := alib.Parse(bytes.NewBufferString(page), baseURL, "text/html")

	// Then
	require.NoError(t, err)
	require.Len(t, books, 1)
	require.Equal(t, "М., 2026 г.", books[0].Bibliography)
	require.Equal(t, 2026, books[0].PublicationYear)
	require.Equal(t, "Seller", books[0].Seller)
	require.Equal(t, "Москва", books[0].Location)
	require.Equal(t, "100 руб.", books[0].Price)
}

func Test_Parse_preserves_bibliography_without_seller_preamble(t *testing.T) {
	t.Parallel()

	// Given
	page := `<p><b>Книга.</b> М., 2026 г. <a href="/bs.php4?bs=Seller">BS - Seller</a>, Москва.
Цена: 100 руб. <a href="/book.html"><b>Купить</b></a></p>`
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	books, err := alib.Parse(bytes.NewBufferString(page), baseURL, "text/html")

	// Then
	require.NoError(t, err)
	require.Len(t, books, 1)
	require.Equal(t, "М., 2026 г.", books[0].Bibliography)
	require.Equal(t, 2026, books[0].PublicationYear)
}

func Test_Parse_does_not_include_buy_link_in_location_when_price_is_missing(t *testing.T) {
	t.Parallel()

	// Given
	page := `<p><b>Книга.</b> М., 2026 г.<br>
(До заказа внимательно прочтите условия продажи продавца
<a href="/bs.php4?bs=Seller">BS - Seller</a>, Москва.)
<a href="/book.html"><b>Купить</b></a></p>`
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	books, err := alib.Parse(bytes.NewBufferString(page), baseURL, "text/html")

	// Then
	require.NoError(t, err)
	require.Len(t, books, 1)
	require.Equal(t, "Москва", books[0].Location)
	require.Empty(t, books[0].Price)
}

func Test_Parse_skips_listings_with_sale_nodes_before_title(t *testing.T) {
	t.Parallel()

	malformedListings := map[string]string{
		"seller before title": `<p><a href="/bs.php4?bs=Seller">BS - Seller</a><br>
<b>Malformed.</b> М., 2026 г. <a href="/malformed.html"><b>Купить</b></a></p>`,
		"buy before title": `<p><a href="/malformed.html"><b>Купить</b></a><br>
<b>Malformed.</b> М., 2026 г.</p>`,
	}
	for name, malformed := range malformedListings {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Given
			page := malformed + `<p><b>Valid.</b> М., 2025 г.
<a href="/valid.html"><b>Купить</b></a></p>`
			baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
			require.NoError(t, err)

			// When
			books, err := alib.Parse(bytes.NewBufferString(page), baseURL, "text/html")

			// Then
			require.NoError(t, err)
			require.Len(t, books, 1)
			require.Equal(t, "Valid.", books[0].Title)
		})
	}
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

func Test_Parse_accepts_empty_windows1251_search_page(t *testing.T) {
	t.Parallel()

	// Given
	page, err := os.ReadFile("testdata/empty.html")
	require.NoError(t, err)
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	books, err := alib.Parse(bytes.NewReader(page), baseURL, "text/html; charset=windows-1251")

	// Then
	require.NoError(t, err)
	require.Empty(t, books)
}

func Test_Parse_rejects_structurally_changed_empty_search_page(t *testing.T) {
	t.Parallel()

	// Given
	page, err := os.ReadFile("testdata/empty.html")
	require.NoError(t, err)
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	books, err := alib.Parse(bytes.NewReader(bytes.Replace(page, []byte(`name="find3"`), []byte(`name="changed"`), 1)),
		baseURL, "text/html; charset=windows-1251")

	// Then
	require.ErrorIs(t, err, alib.ErrNoBooks)
	require.Empty(t, books)
}

func Test_Parse_rejects_search_page_shell_without_empty_result_marker(t *testing.T) {
	t.Parallel()

	// Given
	page := `<a name="beginStr"></a><form action="/find3.php4" name="find3"><select name="sortby"></select></form>`
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	books, err := alib.Parse(bytes.NewBufferString(page), baseURL, "text/html")

	// Then
	require.ErrorIs(t, err, alib.ErrNoBooks)
	require.Empty(t, books)
}

func Test_Parse_rejects_malformed_listing_on_empty_search_page(t *testing.T) {
	t.Parallel()

	// Given
	page, err := os.ReadFile("testdata/empty.html")
	require.NoError(t, err)
	malformed, err := charmap.Windows1251.NewEncoder().Bytes([]byte(`<p><b>Broken.</b> <a><b>Купить</b></a></p>`))
	require.NoError(t, err)
	page = bytes.Replace(page, []byte("</body>"), append(malformed, []byte("</body>")...), 1)
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	books, err := alib.Parse(bytes.NewReader(page), baseURL, "text/html; charset=windows-1251")

	// Then
	require.ErrorIs(t, err, alib.ErrNoBooks)
	require.Empty(t, books)
}

func Test_Parse_rejects_listing_without_buy_link_on_empty_search_page(t *testing.T) {
	t.Parallel()

	// Given
	page, err := os.ReadFile("testdata/empty.html")
	require.NoError(t, err)
	malformed, err := charmap.Windows1251.NewEncoder().Bytes([]byte(`<p><b>Broken.</b> Цена: 100 руб.</p>`))
	require.NoError(t, err)
	page = bytes.Replace(page, []byte("</body>"), append(malformed, []byte("</body>")...), 1)
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	books, err := alib.Parse(bytes.NewReader(page), baseURL, "text/html; charset=windows-1251")

	// Then
	require.ErrorIs(t, err, alib.ErrNoBooks)
	require.Empty(t, books)
}

func Test_Parse_rejects_unrecognized_listing_in_empty_result_region(t *testing.T) {
	t.Parallel()

	// Given
	page, err := os.ReadFile("testdata/empty.html")
	require.NoError(t, err)
	listing := []byte(`<section><strong>Broken.</strong> Price: 100 <a href="/book.html">Order</a></section>`)
	page = bytes.Replace(page, []byte("<hr><hr>"), append(listing, []byte("<hr><hr>")...), 1)
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	books, err := alib.Parse(bytes.NewReader(page), baseURL, "text/html; charset=windows-1251")

	// Then
	require.ErrorIs(t, err, alib.ErrNoBooks)
	require.Empty(t, books)
}

func Test_Parse_rejects_page_with_buy_link_without_href(t *testing.T) {
	t.Parallel()

	// Given
	page := `<p><b>Broken.</b> М., 2026 г. <a><b>Купить</b></a></p>`
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	books, err := alib.Parse(bytes.NewBufferString(page), baseURL, "text/html")

	// Then
	require.ErrorIs(t, err, alib.ErrNoBooks)
	require.Empty(t, books)
}

func Test_ParseWithResult_keeps_valid_listings_when_one_announcement_is_malformed(t *testing.T) {
	t.Parallel()

	// Given
	page := `<p>Обычный текст с <b>выделением</b>, но это не объявление.</p>` +
		`<p><a href="/bs.php4?bs=Seller">BS - Seller</a><br>
<b>Сбойное объявление.</b> М., 2026 г. <a href="/broken.html"><b>Купить</b></a></p>` +
		`<p><b>Рабочая книга.</b> М., 2025 г. <a href="/valid.html"><b>Купить</b></a></p>`
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	result, err := alib.ParseWithResult(bytes.NewBufferString(page), baseURL, "text/html")

	// Then
	require.NoError(t, err)
	require.Equal(t, []string{"https://www.alib.ru/broken.html"}, result.FailedBuyURLs)
	require.Equal(t, []alib.Book{{
		Title:           "Рабочая книга.",
		Bibliography:    "М., 2025 г.",
		PublicationYear: 2025,
		BuyURL:          "https://www.alib.ru/valid.html",
	}}, result.Books)
}

func Test_ParseWithResult_deduplicates_failed_announcements(t *testing.T) {
	t.Parallel()

	// Given
	page := `<p><a href="/bs.php4?bs=Seller">BS - Seller</a><br>
<b>Сбойное объявление.</b> М., 2026 г. <a href="/broken.html"><b>Купить</b></a></p>` +
		`<p><a href="/bs.php4?bs=Seller">BS - Seller</a><br>
<b>Дубликат сбойного объявления.</b> М., 2026 г. <a href="/broken.html"><b>Купить</b></a></p>`
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	result, err := alib.ParseWithResult(bytes.NewBufferString(page), baseURL, "text/html")

	// Then
	require.NoError(t, err)
	require.Empty(t, result.Books)
	require.Equal(t, []string{"https://www.alib.ru/broken.html"}, result.FailedBuyURLs)
}

func Test_ParseWithResult_ignores_failure_when_duplicate_is_parsed_successfully(t *testing.T) {
	t.Parallel()

	// Given
	page := `<p><a href="/bs.php4?bs=Seller">BS - Seller</a><br>
<b>Сначала сбойное объявление.</b> М., 2026 г. <a href="/book.html"><b>Купить</b></a></p>` +
		`<p><b>Успешное объявление.</b> М., 2026 г. <a href="/book.html"><b>Купить</b></a></p>`
	baseURL, err := url.Parse("https://www.alib.ru/tramka.phtml?tnew=7")
	require.NoError(t, err)

	// When
	result, err := alib.ParseWithResult(bytes.NewBufferString(page), baseURL, "text/html")

	// Then
	require.NoError(t, err)
	require.Empty(t, result.FailedBuyURLs)
	require.Len(t, result.Books, 1)
	require.Equal(t, "Успешное объявление.", result.Books[0].Title)
}
