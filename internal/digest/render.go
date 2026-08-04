// Package digest renders Telegram-safe message chunks.
package digest

import (
	"errors"
	"fmt"
	"html"
	"strings"
	"unicode/utf8"

	"github.com/kemmko/alib-fetcher/internal/alib"
)

// ErrMessageTooLong indicates that one listing cannot fit into a message.
var ErrMessageTooLong = errors.New("digest item exceeds message limit")

const titleLimit = 80

// Chunk is one Telegram message and the books acknowledged after it is sent.
type Chunk struct {
	Text  string
	Books []alib.Book
}

// Render formats books as Telegram HTML and splits only between listings.
func Render(books []alib.Book, limit int) ([]Chunk, error) {
	if len(books) == 0 {
		return nil, nil
	}

	const header = "<b>Новые книги на Alib.ru</b>\n\n"
	chunks := make([]Chunk, 0, 1)
	current := Chunk{Text: header, Books: make([]alib.Book, 0)}
	for _, book := range books {
		item := renderBook(book)
		separator := ""
		if len(current.Books) > 0 {
			separator = "\n\n"
		}

		if utf8.RuneCountInString(current.Text+separator+item) > limit {
			if len(current.Books) == 0 {
				return nil, fmt.Errorf("%w: %s", ErrMessageTooLong, book.BuyURL)
			}
			chunks = append(chunks, current)
			current = Chunk{Text: header, Books: make([]alib.Book, 0)}
			separator = ""
		}
		if utf8.RuneCountInString(current.Text+item) > limit {
			return nil, fmt.Errorf("%w: %s", ErrMessageTooLong, book.BuyURL)
		}

		current.Text += separator + item
		current.Books = append(current.Books, book)
	}

	return append(chunks, current), nil
}

func renderBook(book alib.Book) string {
	title := []rune(strings.TrimSpace(book.Title))
	if len(title) > titleLimit {
		title = append(title[:titleLimit-1], '…')
	}

	lines := []string{"<b>" + html.EscapeString(string(title)) + "</b>"}
	details := make([]string, 0, 3)
	for _, value := range []string{book.Price, book.Seller, book.Condition} {
		if value != "" {
			details = append(details, html.EscapeString(value))
		}
	}
	if len(details) > 0 {
		lines = append(lines, strings.Join(details, " · "))
	}
	lines = append(lines, `<a href="`+html.EscapeString(book.BuyURL)+`">Купить</a>`)

	return strings.Join(lines, "\n")
}
