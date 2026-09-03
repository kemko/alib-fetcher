// Package digest renders Telegram-safe message chunks.
package digest

import (
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"

	"github.com/kemko/alib-fetcher/internal/alib"
)

const (
	header                = "<b>Новые книги на Alib.ru</b>"
	lineBreak             = "<br/>"
	sectionBreak          = lineBreak + lineBreak
	listingSeparator      = "<hr/>"
	richMessageBlockLimit = 500
	richMessageMediaLimit = 50
)

// ErrMessageTooLong indicates that one listing cannot fit into a message.
var ErrMessageTooLong = errors.New("digest item exceeds message limit")

// Options controls message size and publication-year highlighting.
//
//nolint:govet // Keep the public rendering options grouped by concern.
type Options struct {
	LocalTime           time.Time
	FreshBooksLowerYear *int
	Limit               int
	SlinkProfile        string
}

// Chunk is one Telegram message and the books acknowledged after it is sent.
type Chunk struct {
	Text  string
	Books []alib.Book
}

// Render formats books as Telegram HTML and splits only between listings.
func Render(books []alib.Book, options Options) ([]Chunk, error) {
	chunks, _, err := render(books, options, false, 0)

	return chunks, err
}

// RenderSendable formats every listing that fits and reports the oversized listings it skipped.
func RenderSendable(books []alib.Book, options Options, previousFailures int) ([]Chunk, []string, error) {
	return render(books, options, true, previousFailures)
}

func render(books []alib.Book, options Options, skipOversized bool, previousFailures int) ([]Chunk, []string, error) {
	if len(books) == 0 {
		if previousFailures > 0 {
			summaryChunks, err := renderFailureSummary(previousFailures, options)

			return summaryChunks, nil, err
		}

		return nil, nil, nil
	}
	if renderedRuneCount(header) > options.Limit {
		return nil, nil, fmt.Errorf("%w: %s", ErrMessageTooLong, books[0].BuyURL)
	}

	chunks := make([]Chunk, 0, 1)
	skippedBuyURLs := make([]string, 0)
	current := Chunk{Text: header, Books: make([]alib.Book, 0)}
	currentBlocks := 0
	currentMedia := 0
	for _, book := range books {
		item, fits := renderItem(book, options)
		if !fits {
			if skipOversized {
				skippedBuyURLs = append(skippedBuyURLs, book.BuyURL)
				continue
			}

			return nil, nil, fmt.Errorf("%w: %s", ErrMessageTooLong, book.BuyURL)
		}
		chunks, current, currentBlocks, currentMedia = appendBook(
			chunks, current, currentBlocks, currentMedia, book, item, options,
		)
	}
	failed := previousFailures + len(skippedBuyURLs)
	if len(current.Books) == 0 {
		if failed == 0 {
			return nil, skippedBuyURLs, nil
		}

		summaryChunks, err := renderFailureSummary(failed, options)

		return summaryChunks, skippedBuyURLs, err
	}
	if failed == 0 {
		return append(chunks, current), skippedBuyURLs, nil
	}

	summary := failureSummary(failed)
	if !chunkExceedsLimits(
		currentBlocks+2,
		currentMedia,
		current.Text+listingSeparator+summary,
		options.Limit,
	) {
		current.Text += listingSeparator + summary
		return append(chunks, current), skippedBuyURLs, nil
	}

	chunks = append(chunks, current)
	return append(chunks, Chunk{Text: summary, Books: make([]alib.Book, 0)}), skippedBuyURLs, nil
}

func renderItem(book alib.Book, options Options) (string, bool) {
	item := renderBook(book, options)
	itemLimit := options.Limit
	if renderedRuneCount(item) > itemLimit && strings.TrimSpace(book.Content) != "" {
		item = truncateContent(book, options)
		itemLimit--
	}

	return item, renderedRuneCount(item) <= itemLimit
}

func appendBook(
	chunks []Chunk,
	current Chunk,
	currentBlocks, currentMedia int,
	book alib.Book,
	item string,
	options Options,
) ([]Chunk, Chunk, int, int) {
	separator := ""
	if len(current.Books) > 0 {
		separator = listingSeparator
	} else if current.Text != "" {
		separator = sectionBreak
	}
	itemBlocks := richMessageBlocks(book, options)
	itemMedia := richMessageMedia(book, options)
	candidateBlocks := currentBlocks + itemBlocks
	if len(current.Books) > 0 {
		candidateBlocks++
	}
	if chunkExceedsLimits(candidateBlocks, currentMedia+itemMedia, current.Text+separator+item, options.Limit) {
		chunks = append(chunks, current)
		current = Chunk{Books: make([]alib.Book, 0)}
		currentBlocks = 0
		currentMedia = 0
		separator = ""
	}

	current.Text += separator + item
	current.Books = append(current.Books, book)
	if len(current.Books) > 1 {
		currentBlocks++
	}
	currentBlocks += itemBlocks
	currentMedia += itemMedia

	return chunks, current, currentBlocks, currentMedia
}

func renderFailureSummary(failed int, options Options) ([]Chunk, error) {
	summary := failureSummary(failed)
	if renderedRuneCount(summary) > options.Limit {
		return nil, fmt.Errorf("%w: failure summary", ErrMessageTooLong)
	}
	if !chunkExceedsLimits(1, 0, header+sectionBreak+summary, options.Limit) {
		return []Chunk{{Text: header + sectionBreak + summary, Books: make([]alib.Book, 0)}}, nil
	}
	if renderedRuneCount(header) > options.Limit {
		return nil, fmt.Errorf("%w: failure summary", ErrMessageTooLong)
	}

	return []Chunk{
		{Text: header, Books: make([]alib.Book, 0)},
		{Text: summary, Books: make([]alib.Book, 0)},
	}, nil
}

func failureSummary(failed int) string {
	return fmt.Sprintf("Не удалось обработать книг: %d", failed)
}

func chunkExceedsLimits(blocks, media int, text string, textLimit int) bool {
	return blocks > richMessageBlockLimit || media > richMessageMediaLimit || renderedRuneCount(text) > textLimit
}

func truncateContent(book alib.Book, options Options) string {
	contentRunes := []rune(strings.TrimSpace(book.Content))
	low, high := 0, len(contentRunes)
	for low < high {
		middle := (low + high + 1) / 2
		candidate := book
		candidate.Content = string(contentRunes[:middle]) + "…"
		if renderedRuneCount(renderBook(candidate, options)) <= options.Limit-1 {
			low = middle
			continue
		}
		high = middle - 1
	}

	book.Content = string(contentRunes[:low]) + "…"

	return renderBook(book, options)
}

func renderedRuneCount(value string) int {
	document, err := xhtml.Parse(strings.NewReader(value))
	if err != nil {
		return 0
	}

	var count func(*xhtml.Node) int
	count = func(node *xhtml.Node) int {
		runes := 0
		if node.Type == xhtml.TextNode {
			runes += len([]rune(node.Data))
		}
		if node.Type == xhtml.ElementNode && node.Data == "br" {
			runes++
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			runes += count(child)
		}

		return runes
	}

	return count(document)
}

func renderBook(book alib.Book, options Options) string {
	mainLine := publicationEmoji(book.PublicationYear, options) +
		"<b>" + renderMultilineText(strings.TrimSpace(book.Title)) + "</b>"
	if bibliography := strings.TrimSpace(book.Bibliography); bibliography != "" {
		mainLine += " " + renderMultilineText(bibliography)
	}

	sections := []string{mainLine}
	if content := strings.TrimSpace(book.Content); content != "" {
		sections = append(sections, renderMultilineText(content))
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
	photoLinks, slideshowPhotos := renderPhotos(book.Photos, options.SlinkProfile)
	if len(photoLinks) > 0 {
		details = append(details, "Смотрите: "+strings.Join(photoLinks, " - "))
	}
	if len(details) > 0 {
		sections = append(sections, strings.Join(details, lineBreak))
	}
	if slideshow := renderSlideshow(slideshowPhotos); slideshow != "" {
		sections = append(sections, slideshow)
	}
	sections = append(sections, renderLink(book.BuyURL, "Купить"))

	return strings.Join(sections, sectionBreak)
}

func renderPhotos(photos []alib.Photo, slinkProfile string) ([]string, []alib.Photo) {
	photoLinks := make([]string, 0, len(photos))
	slideshowPhotos := make([]alib.Photo, 0, len(photos))
	for _, photo := range photos {
		if isPublishedPhoto(photo, slinkProfile) && len(slideshowPhotos) < richMessageMediaLimit {
			slideshowPhotos = append(slideshowPhotos, photo)
			continue
		}
		photoLinks = append(photoLinks, renderLink(photo.URL, photoCaption(photo)))
	}

	return photoLinks, slideshowPhotos
}

func isPublishedPhoto(photo alib.Photo, slinkProfile string) bool {
	return slinkProfile != "" && photo.SlinkURL != "" &&
		photo.SlinkProfile == slinkProfile && !photo.NonImage
}

func photoCaption(photo alib.Photo) string {
	if caption := strings.TrimSpace(photo.Caption); caption != "" {
		return caption
	}

	return "фото"
}

func renderSlideshow(photos []alib.Photo) string {
	if len(photos) == 0 {
		return ""
	}

	parts := []string{"<tg-slideshow>"}
	seenCaptions := make(map[string]struct{}, len(photos))
	captions := make([]string, 0, len(photos))
	for _, photo := range photos {
		parts = append(parts, `<img src="`+escapeLinkTarget(photo.SlinkURL)+`"/>`)
		caption := strings.TrimSpace(photo.Caption)
		if caption == "" {
			continue
		}
		if _, seen := seenCaptions[caption]; seen {
			continue
		}
		seenCaptions[caption] = struct{}{}
		captions = append(captions, caption)
	}
	if len(captions) > 0 {
		parts = append(parts, "<figcaption>"+renderMultilineText(strings.Join(captions, " — "))+"</figcaption>")
	}
	parts = append(parts, "</tg-slideshow>")

	return strings.Join(parts, "")
}

func richMessageBlocks(book alib.Book, options Options) int {
	media := richMessageMedia(book, options)
	if media == 0 {
		return 1
	}

	return media + 3
}

func richMessageMedia(book alib.Book, options Options) int {
	media := 0
	for _, photo := range book.Photos {
		if isPublishedPhoto(photo, options.SlinkProfile) {
			media++
			if media == richMessageMediaLimit {
				return media
			}
		}
	}

	return media
}

func renderMultilineText(value string) string {
	escaped := html.EscapeString(value)
	escaped = strings.ReplaceAll(escaped, "\r\n", "\n")
	escaped = strings.ReplaceAll(escaped, "\r", "\n")

	return strings.ReplaceAll(escaped, "\n", lineBreak)
}

func publicationEmoji(publicationYear int, options Options) string {
	currentYear := options.LocalTime.Year()
	if publicationYear > currentYear {
		return "🛸 "
	}
	if publicationYear == 0 {
		return "🛸 "
	}
	if publicationYear < 0 {
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
