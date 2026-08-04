package alib

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

var (
	pricePattern     = regexp.MustCompile(`Цена:\s*([0-9][0-9\s]*\s*руб\.)`)
	conditionPattern = regexp.MustCompile(`Состояние:\s*(.*?)(?:\s+Смотрите:|$)`)
)

// ErrNoBooks indicates that the source page contained no recognizable listings.
var ErrNoBooks = errors.New("page contains no book listings")

// Parse decodes an Alib.ru page and extracts unique sale listings.
func Parse(reader io.Reader, baseURL *url.URL, contentType string) ([]Book, error) {
	decoded, err := charset.NewReader(reader, contentType)
	if err != nil {
		return nil, fmt.Errorf("decode page: %w", err)
	}

	document, err := html.Parse(decoded)
	if err != nil {
		return nil, fmt.Errorf("parse page: %w", err)
	}

	books := make([]Book, 0)
	seen := make(map[string]struct{})
	for node := range document.Descendants() {
		if node.Type != html.ElementNode || node.Data != "p" {
			continue
		}

		book, found := parseBook(node, baseURL)
		if !found {
			continue
		}
		if _, exists := seen[book.BuyURL]; exists {
			continue
		}

		seen[book.BuyURL] = struct{}{}
		books = append(books, book)
	}

	if len(books) == 0 {
		return nil, ErrNoBooks
	}

	return books, nil
}

func parseBook(node *html.Node, baseURL *url.URL) (Book, bool) {
	var title, seller, buyURL string
	for descendant := range node.Descendants() {
		if descendant.Type != html.ElementNode {
			continue
		}

		switch descendant.Data {
		case "b":
			text := normalizedText(descendant)
			if title == "" && text != "Купить" {
				title = text
			}
		case "a":
			href := attribute(descendant, "href")
			text := normalizedText(descendant)
			if text == "Купить" {
				buyURL = resolveURL(baseURL, href)
			}
			if seller == "" && strings.Contains(href, "bs.php4") {
				seller = text
			}
		}
	}

	if title == "" || buyURL == "" {
		return Book{}, false
	}

	text := normalizedText(node)
	return Book{
		Title:     title,
		Seller:    seller,
		Price:     firstMatch(pricePattern, text),
		Condition: firstMatch(conditionPattern, text),
		BuyURL:    buyURL,
	}, true
}

func normalizedText(node *html.Node) string {
	parts := make([]string, 0)
	for descendant := range node.Descendants() {
		if descendant.Type == html.TextNode {
			parts = append(parts, descendant.Data)
		}
	}

	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

func attribute(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}

	return ""
}

func resolveURL(baseURL *url.URL, raw string) string {
	reference, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	return baseURL.ResolveReference(reference).String()
}

func firstMatch(pattern *regexp.Regexp, text string) string {
	match := pattern.FindStringSubmatch(text)
	if len(match) < 2 {
		return ""
	}

	return strings.TrimSpace(match[1])
}
