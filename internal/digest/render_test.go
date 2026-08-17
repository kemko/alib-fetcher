package digest_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/digest"

	"github.com/stretchr/testify/require"
)

func Test_Render_formats_structured_listing_and_escapes_HTML(t *testing.T) {
	t.Parallel()

	// Given
	lowerYear := 2021
	book := alib.Book{
		Title:           "Автор. A < B & книга.",
		Bibliography:    "Полная библиография & ISBN.\r\nВторая строка.",
		PublicationYear: 2021,
		Content:         "Первая строка <содержания>.\rВторая & строка.",
		Seller:          "Bot & Sad",
		SellerURL:       "https://example.com/seller?a=1&b=2",
		Location:        "Москва",
		Price:           "3 900 руб.",
		Condition:       "Состояние: Отличное.\nКомплект <полный>.",
		BuyURL:          "https://example.com/book?a=1&b=2",
		HasPhotos:       true,
	}
	options := digest.Options{
		Limit:               4096,
		LocalTime:           time.Date(2026, time.August, 5, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60)),
		FreshBooksLowerYear: &lowerYear,
	}

	// When
	chunks, err := digest.Render([]alib.Book{book}, options)

	// Then
	require.NoError(t, err)
	require.Equal(t, []digest.Chunk{{
		Text: `<p><b>Новые книги на Alib.ru</b></p>` +
			`<p>✨ <b>Автор. A &lt; B &amp; книга.</b> Полная библиография &amp; ISBN.<br/>Вторая строка.</p>` +
			`<br/><br/><p>Первая строка &lt;содержания&gt;.<br/>Вторая &amp; строка.</p>` +
			`<br/><br/>` +
			`<p>Продавец: <a href="https://example.com/seller?a=1&amp;b=2">Bot &amp; Sad</a>, Москва.` +
			`<br/>Цена: 3 900 руб.<br/>Состояние: Отличное.<br/>Комплект &lt;полный&gt;.<br/>Фото: есть</p>` +
			`<br/><br/><p><a href="https://example.com/book?a=1&amp;b=2">Купить</a></p>`,
		Books: []alib.Book{book},
	}}, chunks)
	require.NotContains(t, chunks[0].Text, "\r")
	require.NotContains(t, chunks[0].Text, "\n")
}

func Test_Render_normalizes_line_breaks_in_all_dynamic_fields(t *testing.T) {
	t.Parallel()

	// Given
	book := alib.Book{
		Title:     "Первая\r\nВторая",
		Seller:    "Bot\nSad",
		SellerURL: "https://example.com/sell\r\ner",
		Location:  "Моск\rва",
		Price:     "500\nруб.",
		BuyURL:    "https://example.com/bo\nok",
	}

	// When
	chunks, err := digest.Render([]alib.Book{book}, digest.Options{Limit: 4096})

	// Then
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	require.Equal(
		t,
		`<p><b>Новые книги на Alib.ru</b></p>`+
			`<p><b>Первая<br/>Вторая</b></p><br/><br/>`+
			`<p>Продавец: <a href="https://example.com/sell%0D%0Aer">Bot<br/>Sad</a>, Моск<br/>ва.`+
			`<br/>Цена: 500<br/>руб.<br/>Фото: нет</p><br/><br/>`+
			`<p><a href="https://example.com/bo%0Aok">Купить</a></p>`,
		chunks[0].Text,
	)
	require.NotRegexp(t, `[\r\n]`, chunks[0].Text)
}

func Test_Render_highlights_publication_year(t *testing.T) {
	t.Parallel()

	currentYear := 2026
	previousYear := 2025
	thresholdYear := 2021
	tests := []struct {
		localTime time.Time
		name      string
		emoji     string
		bookYear  int
		lowerYear int
		freshness bool
	}{
		{
			name:      "current year is hot without threshold",
			bookYear:  currentYear,
			localTime: time.Date(currentYear, time.August, 1, 0, 0, 0, 0, time.UTC),
			emoji:     "🔥 ",
		},
		{
			name:      "previous year is hot in January without threshold",
			bookYear:  previousYear,
			localTime: time.Date(currentYear, time.January, 31, 0, 0, 0, 0, time.UTC),
			emoji:     "🔥 ",
		},
		{
			name:      "January rule precedes excluding threshold",
			bookYear:  previousYear,
			localTime: time.Date(currentYear, time.January, 1, 0, 0, 0, 0, time.UTC),
			lowerYear: currentYear,
			freshness: true,
			emoji:     "🔥 ",
		},
		{
			name:      "previous year has no emoji when threshold is disabled",
			bookYear:  previousYear,
			localTime: time.Date(currentYear, time.August, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "age threshold makes previous year fresh",
			bookYear:  previousYear,
			localTime: time.Date(currentYear, time.August, 1, 0, 0, 0, 0, time.UTC),
			lowerYear: thresholdYear,
			freshness: true,
			emoji:     "✨ ",
		},
		{
			name:      "since threshold makes included year fresh",
			bookYear:  2023,
			localTime: time.Date(currentYear, time.August, 1, 0, 0, 0, 0, time.UTC),
			lowerYear: thresholdYear,
			freshness: true,
			emoji:     "✨ ",
		},
		{
			name:      "inclusive lower boundary is fresh",
			bookYear:  thresholdYear,
			localTime: time.Date(currentYear, time.August, 1, 0, 0, 0, 0, time.UTC),
			lowerYear: thresholdYear,
			freshness: true,
			emoji:     "✨ ",
		},
		{
			name:      "older book has no emoji",
			bookYear:  thresholdYear - 1,
			localTime: time.Date(currentYear, time.August, 1, 0, 0, 0, 0, time.UTC),
			lowerYear: thresholdYear,
			freshness: true,
		},
		{
			name:      "future book has no emoji",
			bookYear:  currentYear + 1,
			localTime: time.Date(currentYear, time.August, 1, 0, 0, 0, 0, time.UTC),
			lowerYear: thresholdYear,
			freshness: true,
		},
		{
			name:      "book without year has no emoji",
			localTime: time.Date(currentYear, time.August, 1, 0, 0, 0, 0, time.UTC),
			lowerYear: thresholdYear,
			freshness: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// Given
			book := alib.Book{
				Title:           "Книга",
				PublicationYear: test.bookYear,
				BuyURL:          "https://example.com/book",
			}

			// When
			chunks, err := digest.Render([]alib.Book{book}, digest.Options{
				Limit:               4096,
				LocalTime:           test.localTime,
				FreshBooksLowerYear: optionalYear(test.freshness, test.lowerYear),
			})

			// Then
			require.NoError(t, err)
			require.Len(t, chunks, 1)
			require.Equal(
				t,
				`<p><b>Новые книги на Alib.ru</b></p><p>`+test.emoji+`<b>Книга</b></p>`+
					`<br/><br/><p>Фото: нет</p>`+
					`<br/><br/><p><a href="https://example.com/book">Купить</a></p>`,
				chunks[0].Text,
			)
		})
	}
}

func optionalYear(enabled bool, year int) *int {
	if !enabled {
		return nil
	}

	return &year
}

func Test_Render_omits_optional_fields_without_extra_paragraphs(t *testing.T) {
	t.Parallel()

	// Given
	book := alib.Book{
		Title:        "Книга без содержания",
		Bibliography: "Л., 1970 г.",
		Seller:       "BotSad",
		Location:     "Москва",
		Price:        "500 руб.",
		BuyURL:       "https://example.com/book",
	}

	// When
	chunks, err := digest.Render([]alib.Book{book}, digest.Options{Limit: 4096})

	// Then
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	require.Equal(
		t,
		`<p><b>Новые книги на Alib.ru</b></p><p><b>Книга без содержания</b> Л., 1970 г.</p>`+
			`<br/><br/><p>Продавец: BotSad, Москва.<br/>Цена: 500 руб.<br/>Фото: нет</p>`+
			`<br/><br/><p><a href="https://example.com/book">Купить</a></p>`,
		chunks[0].Text,
	)
	require.NotContains(t, chunks[0].Text, "<p></p>")
	require.NotContains(t, chunks[0].Text, "<br/><br/><br/><br/>")
}

func Test_Render_separates_listings_with_divider(t *testing.T) {
	t.Parallel()

	// Given
	books := []alib.Book{
		{Title: "Первая", BuyURL: "https://example.com/1"},
		{Title: "Вторая", BuyURL: "https://example.com/2"},
	}

	// When
	chunks, err := digest.Render(books, digest.Options{Limit: 4096})

	// Then
	require.NoError(t, err)
	require.Equal(t, []digest.Chunk{{
		Text: `<p><b>Новые книги на Alib.ru</b></p>` +
			`<p><b>Первая</b></p><br/><br/><p>Фото: нет</p>` +
			`<br/><br/><p><a href="https://example.com/1">Купить</a></p>` +
			`<hr/>` +
			`<p><b>Вторая</b></p><br/><br/><p>Фото: нет</p>` +
			`<br/><br/><p><a href="https://example.com/2">Купить</a></p>`,
		Books: books,
	}}, chunks)
	require.Equal(t, 1, strings.Count(chunks[0].Text, "<hr/>"))
	require.NotContains(t, chunks[0].Text[:strings.Index(chunks[0].Text, "<b>Первая</b>")], "<hr/>")
	require.NotContains(t, chunks[0].Text[strings.Index(chunks[0].Text, "<b>Вторая</b>"):], "<hr/>")
}

func Test_Render_splits_only_between_complete_listings(t *testing.T) {
	t.Parallel()

	// Given
	books := []alib.Book{
		{Title: "Первая", PublicationYear: 2026, BuyURL: "https://example.com/1"},
		{Title: "Вторая длиннее первой", PublicationYear: 2025, BuyURL: "https://example.com/2"},
		{Title: "Третья тоже длинная", PublicationYear: 2024, BuyURL: "https://example.com/3"},
	}
	localTime := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	header := `<p><b>Новые книги на Alib.ru</b></p>`
	firstMessage := header + `<p>🔥 <b>Первая</b></p>` +
		`<br/><br/><p>Фото: нет</p><br/><br/><p><a href="https://example.com/1">Купить</a></p>`
	secondMessage := `<p>🔥 <b>Вторая длиннее первой</b></p>` +
		`<br/><br/><p>Фото: нет</p><br/><br/><p><a href="https://example.com/2">Купить</a></p>`
	thirdMessage := `<p><b>Третья тоже длинная</b></p>` +
		`<br/><br/><p>Фото: нет</p><br/><br/><p><a href="https://example.com/3">Купить</a></p>`
	messageLimit := utf8.RuneCountInString(firstMessage)
	require.Greater(t, utf8.RuneCountInString(header+secondMessage), messageLimit)

	// When
	chunks, err := digest.Render(books, digest.Options{Limit: messageLimit, LocalTime: localTime})

	// Then
	require.NoError(t, err)
	require.Equal(t, []digest.Chunk{
		{Text: firstMessage, Books: books[:1]},
		{Text: secondMessage, Books: books[1:2]},
		{Text: thirdMessage, Books: books[2:]},
	}, chunks)
	allMessages := strings.Join([]string{chunks[0].Text, chunks[1].Text, chunks[2].Text}, "")
	require.Equal(t, 1, strings.Count(allMessages, "Новые книги на Alib.ru"))
	require.Contains(t, chunks[0].Text, "Новые книги на Alib.ru")
	require.NotContains(t, chunks[1].Text, "Новые книги на Alib.ru")
	require.NotContains(t, chunks[2].Text, "Новые книги на Alib.ru")
	for _, chunk := range chunks {
		require.LessOrEqual(t, utf8.RuneCountInString(chunk.Text), messageLimit)
		require.NotContains(t, chunk.Text, "<hr/>")
	}
}

func Test_Render_uses_header_only_chunk_when_first_listing_fits_only_without_header(t *testing.T) {
	t.Parallel()

	// Given
	book := alib.Book{
		Title:  "Первая длинная книга",
		BuyURL: "https://example.com/1",
	}
	header := `<p><b>Новые книги на Alib.ru</b></p>`
	unlimitedChunks, err := digest.Render([]alib.Book{book}, digest.Options{Limit: 4096})
	require.NoError(t, err)
	require.Len(t, unlimitedChunks, 1)
	require.True(t, strings.HasPrefix(unlimitedChunks[0].Text, header))
	listing := strings.TrimPrefix(unlimitedChunks[0].Text, header)
	messageLimit := utf8.RuneCountInString(listing)
	require.LessOrEqual(t, utf8.RuneCountInString(header), messageLimit)
	require.Greater(t, utf8.RuneCountInString(header+listing), messageLimit)

	// When
	chunks, err := digest.Render([]alib.Book{book}, digest.Options{Limit: messageLimit})

	// Then
	require.NoError(t, err)
	require.Equal(t, []digest.Chunk{
		{Text: header, Books: []alib.Book{}},
		{Text: listing, Books: []alib.Book{book}},
	}, chunks)
}

func Test_Render_counts_divider_toward_message_limit(t *testing.T) {
	t.Parallel()

	// Given
	books := []alib.Book{
		{Title: "Первая", BuyURL: "https://example.com/1"},
		{Title: "Вторая", BuyURL: "https://example.com/2"},
	}
	unlimitedChunks, err := digest.Render(books, digest.Options{Limit: 4096})
	require.NoError(t, err)
	require.Len(t, unlimitedChunks, 1)
	limitWithoutDivider := utf8.RuneCountInString(strings.Replace(unlimitedChunks[0].Text, "<hr/>", "", 1))

	// When
	chunks, err := digest.Render(books, digest.Options{Limit: limitWithoutDivider})

	// Then
	require.NoError(t, err)
	require.Len(t, chunks, 2)
	for _, chunk := range chunks {
		require.LessOrEqual(t, utf8.RuneCountInString(chunk.Text), limitWithoutDivider)
		require.NotContains(t, chunk.Text, "<hr/>")
	}
}

func Test_Render_rejects_listing_over_rune_limit(t *testing.T) {
	t.Parallel()

	// Given
	book := alib.Book{
		Title:  strings.Repeat("Книга ", 20),
		BuyURL: "https://example.com/oversized",
	}

	// When
	chunks, err := digest.Render([]alib.Book{book}, digest.Options{Limit: 64})

	// Then
	require.ErrorIs(t, err, digest.ErrMessageTooLong)
	require.ErrorContains(t, err, book.BuyURL)
	require.Empty(t, chunks)
}

func Test_RenderSendable_skips_oversized_listings_in_one_pass(t *testing.T) {
	t.Parallel()

	// Given
	first := alib.Book{Title: "Первая", BuyURL: "https://example.com/1"}
	oversized := alib.Book{
		Title:  strings.Repeat("Очень длинная книга ", 20),
		BuyURL: "https://example.com/oversized",
	}
	second := alib.Book{Title: "Вторая", BuyURL: "https://example.com/2"}

	// When
	chunks, skippedBuyURLs, err := digest.RenderSendable(
		[]alib.Book{first, oversized, second},
		digest.Options{Limit: 180},
	)

	// Then
	require.NoError(t, err)
	require.Equal(t, []string{oversized.BuyURL}, skippedBuyURLs)
	require.Equal(t, []digest.Chunk{
		{
			Text: `<p><b>Новые книги на Alib.ru</b></p>` +
				`<p><b>Первая</b></p><br/><br/><p>Фото: нет</p>` +
				`<br/><br/><p><a href="https://example.com/1">Купить</a></p>`,
			Books: []alib.Book{first},
		},
		{
			Text: `<p><b>Вторая</b></p><br/><br/><p>Фото: нет</p>` +
				`<br/><br/><p><a href="https://example.com/2">Купить</a></p>`,
			Books: []alib.Book{second},
		},
	}, chunks)
}

func Test_Render_returns_no_chunks_for_no_books(t *testing.T) {
	t.Parallel()

	// When
	chunks, err := digest.Render(nil, digest.Options{Limit: 4096})

	// Then
	require.NoError(t, err)
	require.Nil(t, chunks)
}
