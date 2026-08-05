// Package digest renders Telegram-safe message chunks.
package digest

import (
	"errors"
	"fmt"
	"html"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kemmko/alib-fetcher/internal/alib"
)

// ErrMessageTooLong indicates that one listing cannot fit into a message.
var ErrMessageTooLong = errors.New("digest item exceeds message limit")

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
	var rendered strings.Builder
	rendered.WriteString("<b>" + html.EscapeString(strings.TrimSpace(book.Title)) + "</b>")
	appendText(&rendered, book.TextBeforeSeller)
	if book.Seller != "" {
		appendLink(&rendered, book.SellerURL, book.Seller)
	}
	appendText(&rendered, book.TextBeforeBuy)
	appendLink(&rendered, book.BuyURL, "Купить")
	appendText(&rendered, book.TextAfterBuy)
	if !strings.HasSuffix(rendered.String(), "\n") {
		rendered.WriteByte('\n')
	}
	if book.HasPhotos {
		rendered.WriteString("Фото: есть")
	} else {
		rendered.WriteString("Фото: нет")
	}

	return rendered.String()
}

func appendText(rendered *strings.Builder, value string) {
	if value == "" {
		return
	}
	appendSeparator(rendered, value)
	rendered.WriteString(html.EscapeString(value))
}

func appendLink(rendered *strings.Builder, target, label string) {
	appendSeparator(rendered, label)
	if target == "" {
		rendered.WriteString(html.EscapeString(label))
		return
	}

	rendered.WriteString(`<a href="` + html.EscapeString(target) + `">` + html.EscapeString(label) + `</a>`)
}

func appendSeparator(rendered *strings.Builder, next string) {
	current := rendered.String()
	last, _ := utf8.DecodeLastRuneInString(current)
	first, _ := utf8.DecodeRuneInString(next)
	if current == "" || next == "" || unicode.IsSpace(last) || unicode.IsSpace(first) {
		return
	}
	if strings.ContainsRune(".,;:!?)]}»", first) || strings.ContainsRune("([{«", last) {
		return
	}

	rendered.WriteByte(' ')
}
