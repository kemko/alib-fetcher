package alib

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const lineBreakMarker = "\x00"

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
	var titleNode, sellerNode, buyNode *html.Node
	for descendant := range node.Descendants() {
		if descendant.Type != html.ElementNode {
			continue
		}

		switch descendant.Data {
		case "b":
			text := normalizedText(descendant)
			if titleNode == nil && text != "Купить" {
				titleNode = descendant
			}
		case "a":
			rawHref := href(descendant)
			text := normalizedText(descendant)
			if text == "Купить" {
				buyNode = descendant
			}
			if sellerNode == nil && strings.Contains(rawHref, "bs.php4") {
				sellerNode = descendant
			}
		}
	}

	if titleNode == nil || buyNode == nil {
		return Book{}, false
	}

	buyURL := resolveURL(baseURL, href(buyNode))
	if buyURL == "" {
		return Book{}, false
	}

	fragments := listingFragments(node, titleNode, sellerNode, buyNode)
	textAfterBuy := removePhotoSection(normalizedFragment(fragments[2]))
	seller, sellerURL := "", ""
	if sellerNode != nil {
		seller = normalizedText(sellerNode)
		sellerURL = resolveURL(baseURL, href(sellerNode))
	}

	return Book{
		Title:            normalizedText(titleNode),
		TextBeforeSeller: normalizedFragment(fragments[0]),
		Seller:           seller,
		SellerURL:        sellerURL,
		TextBeforeBuy:    normalizedFragment(fragments[1]),
		BuyURL:           buyURL,
		TextAfterBuy:     textAfterBuy,
		HasPhotos:        hasPhotoLink(node),
	}, true
}

func listingFragments(node, titleNode, sellerNode, buyNode *html.Node) [3]string {
	var fragments [3]strings.Builder
	section := -1
	var visit func(*html.Node)
	visit = func(current *html.Node) {
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			switch child {
			case titleNode:
				section = 0
				continue
			case sellerNode:
				section = 1
				continue
			case buyNode:
				section = 2
				continue
			}

			if section >= 0 && child.Type == html.ElementNode && child.Data == "br" {
				fragments[section].WriteString(lineBreakMarker)
				continue
			}
			if section >= 0 && child.Type == html.TextNode {
				fragments[section].WriteString(child.Data)
				continue
			}

			visit(child)
		}
	}
	visit(node)

	return [3]string{fragments[0].String(), fragments[1].String(), fragments[2].String()}
}

func normalizedFragment(fragment string) string {
	lines := strings.Split(fragment, lineBreakMarker)
	for index, line := range lines {
		lines[index] = strings.Join(strings.Fields(line), " ")
	}

	return strings.Trim(strings.Join(lines, "\n"), " ")
}

func removePhotoSection(text string) string {
	const marker = "Смотрите:"
	before, _, found := strings.Cut(text, marker)
	if !found {
		return text
	}

	return strings.TrimRight(before, " \n")
}

func hasPhotoLink(node *html.Node) bool {
	for descendant := range node.Descendants() {
		if descendant.Type == html.ElementNode && descendant.Data == "a" &&
			strings.Contains(href(descendant), "foto.php4") {
			return true
		}
	}

	return false
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

func href(node *html.Node) string {
	for _, attr := range node.Attr {
		if attr.Key == "href" {
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
