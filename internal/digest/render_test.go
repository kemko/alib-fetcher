package digest_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	htmlparser "golang.org/x/net/html"

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
		PhotoURLs: []string{
			"https://example.com/photo?id=1&size=large",
			"https://example.com/photo?id=2",
		},
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
		Text: `<b>Новые книги на Alib.ru</b><br/><br/>` +
			`✨ <b>Автор. A &lt; B &amp; книга.</b> Полная библиография &amp; ISBN.<br/>Вторая строка.` +
			`<br/><br/>Первая строка &lt;содержания&gt;.<br/>Вторая &amp; строка.` +
			`<br/><br/>` +
			`Продавец: <a href="https://example.com/seller?a=1&amp;b=2">Bot &amp; Sad</a>, Москва.` +
			`<br/>Цена: 3 900 руб.<br/>Состояние: Отличное.<br/>Комплект &lt;полный&gt;.` +
			`<br/>Смотрите: <a href="https://example.com/photo?id=1&amp;size=large">фото</a> - ` +
			`<a href="https://example.com/photo?id=2">фото</a>` +
			`<br/><br/><a href="https://example.com/book?a=1&amp;b=2">Купить</a>`,
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
		`<b>Новые книги на Alib.ru</b><br/><br/>`+
			`🛸 <b>Первая<br/>Вторая</b><br/><br/>`+
			`Продавец: <a href="https://example.com/sell%0D%0Aer">Bot<br/>Sad</a>, Моск<br/>ва.`+
			`<br/>Цена: 500<br/>руб.<br/><br/>`+
			`<a href="https://example.com/bo%0Aok">Купить</a>`,
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
			name:      "future book is marked without threshold",
			bookYear:  currentYear + 1,
			localTime: time.Date(currentYear, time.August, 1, 0, 0, 0, 0, time.UTC),
			emoji:     "🛸 ",
		},
		{
			name:      "future book is marked with threshold",
			bookYear:  currentYear + 100,
			localTime: time.Date(currentYear, time.August, 1, 0, 0, 0, 0, time.UTC),
			lowerYear: thresholdYear,
			freshness: true,
			emoji:     "🛸 ",
		},
		{
			name:      "book without year is marked as unknown future",
			localTime: time.Date(currentYear, time.August, 1, 0, 0, 0, 0, time.UTC),
			lowerYear: thresholdYear,
			freshness: true,
			emoji:     "🛸 ",
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
				`<b>Новые книги на Alib.ru</b><br/><br/>`+test.emoji+`<b>Книга</b>`+
					`<br/><br/><a href="https://example.com/book">Купить</a>`,
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

func Test_Render_omits_optional_fields_without_extra_sections(t *testing.T) {
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
		`<b>Новые книги на Alib.ru</b><br/><br/>🛸 <b>Книга без содержания</b> Л., 1970 г.`+
			`<br/><br/>Продавец: BotSad, Москва.<br/>Цена: 500 руб.`+
			`<br/><br/><a href="https://example.com/book">Купить</a>`,
		chunks[0].Text,
	)
	require.NotContains(t, chunks[0].Text, "<br/><br/><br/>")
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
		Text: `<b>Новые книги на Alib.ru</b><br/><br/>` +
			`🛸 <b>Первая</b>` +
			`<br/><br/><a href="https://example.com/1">Купить</a>` +
			`<hr/>` +
			`🛸 <b>Вторая</b>` +
			`<br/><br/><a href="https://example.com/2">Купить</a>`,
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
	header := `<b>Новые книги на Alib.ru</b>`
	firstMessage := header + `<br/><br/>🔥 <b>Первая</b>` +
		`<br/><br/><a href="https://example.com/1">Купить</a>`
	secondMessage := `🔥 <b>Вторая длиннее первой</b>` +
		`<br/><br/><a href="https://example.com/2">Купить</a>`
	thirdMessage := `<b>Третья тоже длинная</b>` +
		`<br/><br/><a href="https://example.com/3">Купить</a>`
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
	header := `<b>Новые книги на Alib.ru</b>`
	unlimitedChunks, err := digest.Render([]alib.Book{book}, digest.Options{Limit: 4096})
	require.NoError(t, err)
	require.Len(t, unlimitedChunks, 1)
	require.True(t, strings.HasPrefix(unlimitedChunks[0].Text, header+`<br/><br/>`))
	listing := strings.TrimPrefix(unlimitedChunks[0].Text, header+`<br/><br/>`)
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
		digest.Options{Limit: 150},
	)

	// Then
	require.NoError(t, err)
	require.Equal(t, []string{oversized.BuyURL}, skippedBuyURLs)
	require.Equal(t, []digest.Chunk{
		{
			Text: `<b>Новые книги на Alib.ru</b><br/><br/>` +
				`🛸 <b>Первая</b>` +
				`<br/><br/><a href="https://example.com/1">Купить</a>`,
			Books: []alib.Book{first},
		},
		{
			Text: `🛸 <b>Вторая</b>` +
				`<br/><br/><a href="https://example.com/2">Купить</a>`,
			Books: []alib.Book{second},
		},
	}, chunks)
}

func Test_Render_truncates_long_content_to_limit_minus_one(t *testing.T) {
	t.Parallel()

	// Given
	prefix := strings.Repeat("Описание ", 8)
	book := alib.Book{
		Title:        "Книга",
		Bibliography: "Библиография",
		Content:      prefix + "и ещё " + strings.Repeat("длинное описание ", 20),
		Seller:       "Продавец",
		Location:     "Москва",
		Price:        "500 руб.",
		BuyURL:       "https://example.com/book",
	}
	candidate := book
	candidate.Content = prefix + "…"
	candidateChunks, err := digest.Render([]alib.Book{candidate}, digest.Options{Limit: 4096})
	require.NoError(t, err)
	listing := strings.TrimPrefix(candidateChunks[0].Text, `<b>Новые книги на Alib.ru</b><br/><br/>`)
	messageLimit := utf8.RuneCountInString(listing) + 1

	// When
	chunks, err := digest.Render([]alib.Book{book}, digest.Options{Limit: messageLimit})
	sendableChunks, skippedBuyURLs, sendableErr := digest.RenderSendable(
		[]alib.Book{book},
		digest.Options{Limit: messageLimit},
	)

	// Then
	require.NoError(t, err)
	require.NoError(t, sendableErr)
	require.Empty(t, skippedBuyURLs)
	require.Len(t, chunks, 2)
	require.Equal(t, chunks, sendableChunks)
	require.Equal(t, []alib.Book{}, chunks[0].Books)
	require.Equal(t, []alib.Book{book}, chunks[1].Books)
	require.Equal(t, messageLimit-1, utf8.RuneCountInString(chunks[1].Text))
	require.Contains(t, chunks[1].Text, prefix+"…")
	require.NotContains(t, chunks[1].Text, "и ещё")
}

func Test_Render_truncates_content_at_escaped_rune_boundary(t *testing.T) {
	t.Parallel()

	// Given
	book := alib.Book{
		Title:   "Книга",
		Content: "абв<ёж&зий" + strings.Repeat("длинное описание ", 20),
		BuyURL:  "https://example.com/book",
	}
	candidate := book
	candidate.Content = "абв…"
	candidateChunks, err := digest.Render([]alib.Book{candidate}, digest.Options{Limit: 4096})
	require.NoError(t, err)
	listing := strings.TrimPrefix(candidateChunks[0].Text, `<b>Новые книги на Alib.ru</b><br/><br/>`)
	messageLimit := utf8.RuneCountInString(listing) + 2

	// When
	chunks, err := digest.Render([]alib.Book{book}, digest.Options{Limit: messageLimit})

	// Then
	require.NoError(t, err)
	require.Len(t, chunks, 2)
	rendered := chunks[1].Text
	require.Equal(t, messageLimit-2, utf8.RuneCountInString(rendered))
	require.Contains(t, rendered, "абв…")
	require.NotContains(t, rendered, "&lt;")
	require.NotContains(t, rendered, "абв<")
	_, err = htmlparser.Parse(strings.NewReader("<html><body>" + rendered + "</body></html>"))
	require.NoError(t, err)
}

func Test_Render_truncates_content_by_source_runes_without_mutating_chunk_book(t *testing.T) {
	t.Parallel()

	// Given
	book := alib.Book{
		Title:   "Книга",
		Content: "начало &lt; середина < конец " + strings.Repeat("текст ", 20),
		BuyURL:  "https://example.com/book",
	}
	candidate := book
	candidate.Content = "начало &…"
	candidateChunks, err := digest.Render([]alib.Book{candidate}, digest.Options{Limit: 4096})
	require.NoError(t, err)
	listing := strings.TrimPrefix(candidateChunks[0].Text, `<b>Новые книги на Alib.ru</b><br/><br/>`)
	messageLimit := utf8.RuneCountInString(listing) + 1

	// When
	chunks, err := digest.Render([]alib.Book{book}, digest.Options{Limit: messageLimit})

	// Then
	require.NoError(t, err)
	require.Len(t, chunks, 2)
	require.Equal(t, []alib.Book{book}, chunks[1].Books)
	require.Contains(t, chunks[1].Text, "начало &amp;…")
	require.NotContains(t, chunks[1].Text, "начало &amp;lt;")
	require.Equal(t, messageLimit-1, utf8.RuneCountInString(chunks[1].Text))
}

func Test_Render_returns_no_chunks_for_no_books(t *testing.T) {
	t.Parallel()

	// When
	chunks, err := digest.Render(nil, digest.Options{Limit: 4096})

	// Then
	require.NoError(t, err)
	require.Nil(t, chunks)
}
