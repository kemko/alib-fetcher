package digest_test

import (
	"strings"
	"testing"
	"time"

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
		Bibliography:    "Полная библиография & ISBN.",
		PublicationYear: 2021,
		Content:         "Первая строка <содержания>.\nВторая & строка.",
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
		Text: `<b>Новые книги на Alib.ru</b>

✨ <b>Автор. A &lt; B &amp; книга.</b> Полная библиография &amp; ISBN.

Первая строка &lt;содержания&gt;.
Вторая &amp; строка.

Продавец: <a href="https://example.com/seller?a=1&amp;b=2">Bot &amp; Sad</a>, Москва.
Цена: 3 900 руб.
Состояние: Отличное.
Комплект &lt;полный&gt;.
Фото: есть

<a href="https://example.com/book?a=1&amp;b=2">Купить</a>`,
		Books: []alib.Book{book},
	}}, chunks)
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
			require.Equal(t, `<b>Новые книги на Alib.ru</b>

`+test.emoji+`<b>Книга</b>

Фото: нет

<a href="https://example.com/book">Купить</a>`, chunks[0].Text)
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
	require.Equal(t, `<b>Новые книги на Alib.ru</b>

<b>Книга без содержания</b> Л., 1970 г.

Продавец: BotSad, Москва.
Цена: 500 руб.
Фото: нет

<a href="https://example.com/book">Купить</a>`, chunks[0].Text)
	require.NotContains(t, chunks[0].Text, "\n\n\n")
}

func Test_Render_splits_only_between_complete_listings(t *testing.T) {
	t.Parallel()

	// Given
	books := []alib.Book{
		{Title: "Первая", PublicationYear: 2026, BuyURL: "https://example.com/1"},
		{Title: "Вторая", PublicationYear: 2025, BuyURL: "https://example.com/2"},
	}
	localTime := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	firstMessage := `<b>Новые книги на Alib.ru</b>

🔥 <b>Первая</b>

Фото: нет

<a href="https://example.com/1">Купить</a>`
	secondMessage := `<b>Новые книги на Alib.ru</b>

🔥 <b>Вторая</b>

Фото: нет

<a href="https://example.com/2">Купить</a>`
	messageLimit := len([]rune(firstMessage))

	// When
	chunks, err := digest.Render(books, digest.Options{Limit: messageLimit, LocalTime: localTime})

	// Then
	require.NoError(t, err)
	require.Equal(t, []digest.Chunk{
		{Text: firstMessage, Books: books[:1]},
		{Text: secondMessage, Books: books[1:]},
	}, chunks)
	for _, chunk := range chunks {
		require.LessOrEqual(t, len([]rune(chunk.Text)), messageLimit)
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

func Test_Render_returns_no_chunks_for_no_books(t *testing.T) {
	t.Parallel()

	// When
	chunks, err := digest.Render(nil, digest.Options{Limit: 4096})

	// Then
	require.NoError(t, err)
	require.Nil(t, chunks)
}
