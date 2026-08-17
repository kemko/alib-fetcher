// Package digest renders Telegram-safe message chunks.
package digest

import (
	"errors"
	"fmt"
	"html"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kemko/alib-fetcher/internal/alib"
)

const (
	header           = "<p><b>Новые книги на Alib.ru</b></p>"
	lineBreak        = "<br>"
	listingSeparator = "<hr/>"
)

// ErrMessageTooLong indicates that one listing cannot fit into a message.
var ErrMessageTooLong = errors.New("digest item exceeds message limit")

// Options controls message size and publication-year highlighting.
type Options struct {
	LocalTime           time.Time
	FreshBooksLowerYear *int
	Limit               int
}

// Chunk is one Telegram message and the books acknowledged after it is sent.
type Chunk struct {
	Text  string
	Books []alib.Book
}

// Render formats books as Telegram HTML and splits only between listings.
func Render(books []alib.Book, options Options) ([]Chunk, error) {
	if len(books) == 0 {
		return nil, nil
	}

	chunks := make([]Chunk, 0, 1)
	current := Chunk{Text: header, Books: make([]alib.Book, 0)}
	for _, book := range books {
		item := renderBook(book, options)
		separator := ""
		if len(current.Books) > 0 {
			separator = listingSeparator
		}

		if utf8.RuneCountInString(current.Text+separator+item) > options.Limit {
			if len(current.Books) == 0 {
				return nil, fmt.Errorf("%w: %s", ErrMessageTooLong, book.BuyURL)
			}
			chunks = append(chunks, current)
			current = Chunk{Books: make([]alib.Book, 0)}
			separator = ""
		}
		if utf8.RuneCountInString(current.Text+item) > options.Limit {
			return nil, fmt.Errorf("%w: %s", ErrMessageTooLong, book.BuyURL)
		}

		current.Text += separator + item
		current.Books = append(current.Books, book)
	}

	return append(chunks, current), nil
}

func renderBook(book alib.Book, options Options) string {
	mainLine := publicationEmoji(book.PublicationYear, options) +
		"<b>" + html.EscapeString(strings.TrimSpace(book.Title)) + "</b>"
	if bibliography := strings.TrimSpace(book.Bibliography); bibliography != "" {
		mainLine += " " + html.EscapeString(bibliography)
	}

	paragraphs := []string{"<p>" + mainLine + "</p>"}
	if content := strings.TrimSpace(book.Content); content != "" {
		paragraphs = append(paragraphs, "<p>"+renderMultilineText(content)+"</p>")
	}

	details := make([]string, 0, 4)
	if seller := strings.TrimSpace(book.Seller); seller != "" {
		details = append(details, renderSeller(seller, book.SellerURL, book.Location))
	}
	if price := strings.TrimSpace(book.Price); price != "" {
		details = append(details, "Цена: "+html.EscapeString(price))
	}
	if condition := strings.TrimSpace(book.Condition); condition != "" {
		details = append(details, renderMultilineText(condition))
	}
	if book.HasPhotos {
		details = append(details, "Фото: есть")
	} else {
		details = append(details, "Фото: нет")
	}
	paragraphs = append(paragraphs, "<p>"+strings.Join(details, lineBreak)+"</p>")
	paragraphs = append(paragraphs, "<p>"+renderLink(book.BuyURL, "Купить")+"</p>")

	return strings.Join(paragraphs, "")
}

func renderMultilineText(value string) string {
	escaped := html.EscapeString(value)
	escaped = strings.ReplaceAll(escaped, "\r\n", "\n")
	escaped = strings.ReplaceAll(escaped, "\r", "\n")

	return strings.ReplaceAll(escaped, "\n", lineBreak)
}

func publicationEmoji(publicationYear int, options Options) string {
	currentYear := options.LocalTime.Year()
	if publicationYear <= 0 || publicationYear > currentYear {
		return ""
	}
	if publicationYear == currentYear ||
		(options.LocalTime.Month() == time.January && publicationYear == currentYear-1) {
		return "🔥 "
	}
	if options.FreshBooksLowerYear != nil && publicationYear >= *options.FreshBooksLowerYear {
		return "✨ "
	}

	return ""
}

func renderSeller(seller, sellerURL, location string) string {
	rendered := "Продавец: " + renderLink(sellerURL, seller)
	if location = strings.TrimSpace(location); location != "" {
		rendered += ", " + html.EscapeString(location)
	}
	if !strings.HasSuffix(rendered, ".") {
		rendered += "."
	}

	return rendered
}

func renderLink(target, label string) string {
	if target == "" {
		return html.EscapeString(label)
	}

	return `<a href="` + html.EscapeString(target) + `">` + html.EscapeString(label) + `</a>`
}
