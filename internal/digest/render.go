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
	lineBreak        = "<br/>"
	paragraphBreak   = lineBreak + lineBreak
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
	chunks, _, err := render(books, options, false)

	return chunks, err
}

// RenderSendable formats every listing that fits and reports the oversized listings it skipped.
func RenderSendable(books []alib.Book, options Options) ([]Chunk, []string, error) {
	return render(books, options, true)
}

func render(books []alib.Book, options Options, skipOversized bool) ([]Chunk, []string, error) {
	if len(books) == 0 {
		return nil, nil, nil
	}
	if utf8.RuneCountInString(header) > options.Limit {
		return nil, nil, fmt.Errorf("%w: %s", ErrMessageTooLong, books[0].BuyURL)
	}

	chunks := make([]Chunk, 0, 1)
	skippedBuyURLs := make([]string, 0)
	current := Chunk{Text: header, Books: make([]alib.Book, 0)}
	for _, book := range books {
		item := renderBook(book, options)
		if utf8.RuneCountInString(item) > options.Limit {
			if skipOversized {
				skippedBuyURLs = append(skippedBuyURLs, book.BuyURL)
				continue
			}

			return nil, nil, fmt.Errorf("%w: %s", ErrMessageTooLong, book.BuyURL)
		}
		separator := ""
		if len(current.Books) > 0 {
			separator = listingSeparator
		}

		if utf8.RuneCountInString(current.Text+separator+item) > options.Limit {
			chunks = append(chunks, current)
			current = Chunk{Books: make([]alib.Book, 0)}
			separator = ""
		}

		current.Text += separator + item
		current.Books = append(current.Books, book)
	}
	if len(current.Books) == 0 {
		return nil, skippedBuyURLs, nil
	}

	return append(chunks, current), skippedBuyURLs, nil
}

func renderBook(book alib.Book, options Options) string {
	mainLine := publicationEmoji(book.PublicationYear, options) +
		"<b>" + renderMultilineText(strings.TrimSpace(book.Title)) + "</b>"
	if bibliography := strings.TrimSpace(book.Bibliography); bibliography != "" {
		mainLine += " " + renderMultilineText(bibliography)
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
		details = append(details, "Цена: "+renderMultilineText(price))
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

	return strings.Join(paragraphs, paragraphBreak)
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
		rendered += ", " + renderMultilineText(location)
	}
	if !strings.HasSuffix(rendered, ".") {
		rendered += "."
	}

	return rendered
}

func renderLink(target, label string) string {
	if target == "" {
		return renderMultilineText(label)
	}

	return `<a href="` + escapeLinkTarget(target) + `">` + renderMultilineText(label) + `</a>`
}

func escapeLinkTarget(target string) string {
	target = strings.ReplaceAll(target, "\r", "%0D")
	target = strings.ReplaceAll(target, "\n", "%0A")

	return html.EscapeString(target)
}
