package alib

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const (
	conditionLabel = "Состояние:"
	photoLabel     = "Смотрите:"
	priceLabel     = "Цена:"
	sellerPrefix   = "BS - "
)

// ErrNoBooks indicates that the source page contained no recognizable listings.
var ErrNoBooks = errors.New("page contains no book listings")

type listingPart struct {
	node *html.Node
	text string
}

type listingPosition struct {
	line int
	part int
}

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
	titleNode, sellerNode, buyNode := listingNodes(node)
	if titleNode == nil || buyNode == nil {
		return Book{}, false
	}

	buyURL := resolveURL(baseURL, href(buyNode))
	if buyURL == "" {
		return Book{}, false
	}

	lines := logicalLines(node, titleNode)
	titlePosition, titleFound := findNode(lines, titleNode)
	buyPosition, buyFound := findNode(lines, buyNode)
	if !titleFound || !buyFound {
		return Book{}, false
	}

	sellerPosition, sellerFound := findNode(lines, sellerNode)
	bibliography := parseBibliography(lines, titlePosition, buyPosition, sellerPosition, sellerFound)
	content, condition := parseDescription(lines, buyPosition)
	seller, sellerURL, location := parseSeller(lines, sellerNode, sellerPosition, sellerFound, baseURL)

	return Book{
		Title:           normalizedText(titleNode),
		Bibliography:    bibliography,
		PublicationYear: parsePublicationYear(bibliography),
		Content:         content,
		Seller:          seller,
		SellerURL:       sellerURL,
		Location:        location,
		Price:           parsePrice(lines[buyPosition.line], buyPosition.part),
		Condition:       condition,
		BuyURL:          buyURL,
		HasPhotos:       hasPhotoLink(node),
	}, true
}

func listingNodes(node *html.Node) (*html.Node, *html.Node, *html.Node) {
	var titleNode, sellerNode, buyNode *html.Node
	for descendant := range node.Descendants() {
		if descendant.Type != html.ElementNode {
			continue
		}

		switch descendant.Data {
		case "b":
			if titleNode == nil && normalizedText(descendant) != "Купить" {
				titleNode = descendant
			}
		case "a":
			rawHref := href(descendant)
			if normalizedText(descendant) == "Купить" {
				buyNode = descendant
			}
			if sellerNode == nil && strings.Contains(rawHref, "bs.php4") {
				sellerNode = descendant
			}
		}
	}

	return titleNode, sellerNode, buyNode
}

func logicalLines(node, titleNode *html.Node) [][]listingPart {
	lines := [][]listingPart{nil}
	var visit func(*html.Node)
	visit = func(current *html.Node) {
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && child.Data == "br" {
				lines = append(lines, nil)
				continue
			}
			if child == titleNode || child.Type == html.ElementNode && child.Data == "a" {
				lines[len(lines)-1] = append(lines[len(lines)-1], listingPart{
					node: child,
					text: textContent(child),
				})
				continue
			}
			if child.Type == html.TextNode {
				lines[len(lines)-1] = append(lines[len(lines)-1], listingPart{text: child.Data})
				continue
			}

			visit(child)
		}
	}
	visit(node)

	return lines
}

func findNode(lines [][]listingPart, node *html.Node) (listingPosition, bool) {
	if node == nil {
		return listingPosition{}, false
	}
	for lineIndex, line := range lines {
		for partIndex, part := range line {
			if part.node == node {
				return listingPosition{line: lineIndex, part: partIndex}, true
			}
		}
	}

	return listingPosition{}, false
}

func parseBibliography(
	lines [][]listingPart,
	titlePosition, buyPosition, sellerPosition listingPosition,
	sellerFound bool,
) string {
	saleLine := buyPosition.line
	if sellerFound && sellerPosition.line < saleLine {
		saleLine = sellerPosition.line
	}

	parsedLines := make([]string, 0, saleLine-titlePosition.line+1)
	for lineIndex := titlePosition.line; lineIndex <= saleLine; lineIndex++ {
		start := 0
		if lineIndex == titlePosition.line {
			start = titlePosition.part + 1
		}
		end := len(lines[lineIndex])
		if lineIndex == saleLine {
			if sellerFound && sellerPosition.line == saleLine {
				end = start
			} else {
				end = buyPosition.part
			}
		}
		if start >= end {
			continue
		}

		line := normalizedParts(lines[lineIndex][start:end])
		if lineIndex == saleLine {
			line, _, _ = strings.Cut(line, priceLabel)
		}
		if line = strings.TrimSpace(line); line != "" {
			parsedLines = append(parsedLines, line)
		}
	}

	return strings.Join(parsedLines, "\n")
}

func parseSeller(
	lines [][]listingPart,
	sellerNode *html.Node,
	sellerPosition listingPosition,
	sellerFound bool,
	baseURL *url.URL,
) (string, string, string) {
	if !sellerFound {
		return "", "", ""
	}

	line := lines[sellerPosition.line]
	location := normalizedParts(line[sellerPosition.part+1:])
	if beforePrice, _, found := strings.Cut(location, priceLabel); found {
		location = beforePrice
	}

	return strings.TrimPrefix(normalizedText(sellerNode), sellerPrefix),
		resolveURL(baseURL, href(sellerNode)), cleanLocation(location)
}

func cleanLocation(location string) string {
	return strings.Trim(strings.TrimSpace(location), " ,.;:()")
}

func parsePrice(line []listingPart, buyPart int) string {
	beforeBuy := normalizedParts(line[:buyPart])
	priceStart := strings.LastIndex(beforeBuy, priceLabel)
	if priceStart < 0 {
		return ""
	}

	return strings.TrimSpace(beforeBuy[priceStart+len(priceLabel):])
}

func parseDescription(lines [][]listingPart, buyPosition listingPosition) (string, string) {
	contentLines := make([]string, 0)
	conditionLines := make([]string, 0)
	conditionFound := false
	for lineIndex := buyPosition.line; lineIndex < len(lines); lineIndex++ {
		start := 0
		if lineIndex == buyPosition.line {
			start = buyPosition.part + 1
		}
		line := normalizedParts(lines[lineIndex][start:])
		if beforePhotos, _, found := strings.Cut(line, photoLabel); found {
			line = beforePhotos
			if strings.TrimSpace(line) == "" {
				break
			}
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if !conditionFound {
			beforeCondition, afterLabel, found := strings.Cut(line, conditionLabel)
			if !found {
				contentLines = append(contentLines, line)
				continue
			}
			if beforeCondition = strings.TrimSpace(beforeCondition); beforeCondition != "" {
				contentLines = append(contentLines, beforeCondition)
			}
			line = conditionLabel
			if afterLabel = strings.TrimSpace(afterLabel); afterLabel != "" {
				line += " " + afterLabel
			}
			conditionFound = true
		}

		conditionLines = append(conditionLines, line)
	}

	return strings.Join(contentLines, "\n"), strings.Join(conditionLines, "\n")
}

func parsePublicationYear(bibliography string) int {
	characters := []rune(bibliography)
	lastYear := 0
	for index := 0; index < len(characters); {
		if !isASCIIDigit(characters[index]) {
			index++
			continue
		}

		digitEnd := index
		for digitEnd < len(characters) && isASCIIDigit(characters[digitEnd]) {
			digitEnd++
		}
		if digitEnd-index == 4 && hasYearSuffix(characters, digitEnd) {
			year := 0
			for _, digit := range characters[index:digitEnd] {
				year = year*10 + int(digit-'0')
			}
			if year >= 1000 {
				lastYear = year
			}
		}
		index = digitEnd
	}

	return lastYear
}

func hasYearSuffix(characters []rune, index int) bool {
	for index < len(characters) && unicode.IsSpace(characters[index]) {
		index++
	}
	if index >= len(characters) || characters[index] != 'г' {
		return false
	}
	index++
	if index < len(characters) && characters[index] == '.' {
		return true
	}

	return index == len(characters) || !unicode.IsLetter(characters[index]) && !unicode.IsDigit(characters[index])
}

func isASCIIDigit(character rune) bool {
	return character >= '0' && character <= '9'
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

func normalizedParts(parts []listingPart) string {
	textParts := make([]string, 0, len(parts))
	for _, part := range parts {
		textParts = append(textParts, part.text)
	}

	return strings.Join(strings.Fields(strings.Join(textParts, "")), " ")
}

func normalizedText(node *html.Node) string {
	return strings.Join(strings.Fields(textContent(node)), " ")
}

func textContent(node *html.Node) string {
	parts := make([]string, 0)
	for descendant := range node.Descendants() {
		if descendant.Type == html.TextNode {
			parts = append(parts, descendant.Data)
		}
	}

	return strings.Join(parts, "")
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
