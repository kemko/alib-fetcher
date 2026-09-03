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

// ParseResult contains successfully parsed books and failed listing identities.
type ParseResult struct {
	Books         []Book
	FailedBuyURLs []string
}

type listingPart struct {
	node *html.Node
	text string
}

type listingPosition struct {
	line int
	part int
}

type emptySearchPageMarkers struct {
	beginResultsFound      bool
	emptyResultRegionFound bool
	searchFormFound        bool
	sortControlFound       bool
}

// Parse decodes an Alib.ru page and extracts unique sale listings.
func Parse(reader io.Reader, baseURL *url.URL, contentType string) ([]Book, error) {
	result, err := ParseWithResult(reader, baseURL, contentType)
	if err != nil {
		return nil, err
	}

	return result.Books, nil
}

// ParseWithResult decodes an Alib.ru page and returns parsed listings plus failed listing identities.
func ParseWithResult(reader io.Reader, baseURL *url.URL, contentType string) (ParseResult, error) {
	decoded, err := charset.NewReader(reader, contentType)
	if err != nil {
		return ParseResult{}, fmt.Errorf("decode page: %w", err)
	}

	document, err := html.Parse(decoded)
	if err != nil {
		return ParseResult{}, fmt.Errorf("parse page: %w", err)
	}

	state := parseState{
		books:              make([]Book, 0),
		seen:               make(map[string]struct{}),
		failed:             make(map[string]struct{}),
		failedOrder:        make([]string, 0),
		recognizedListings: make(map[string]struct{}),
	}
	unresolvedCandidate := false
	for node := range document.Descendants() {
		if node.Type != html.ElementNode || node.Data != "p" {
			continue
		}
		if collectListing(&state, node, baseURL) {
			unresolvedCandidate = true
		}
	}

	if len(state.books) == 0 {
		if unresolvedCandidate ||
			(len(state.recognizedListings) == 0 && len(state.failed) == 0 && !isEmptySearchResultsPage(document)) {
			return ParseResult{}, ErrNoBooks
		}
	}

	failedBuyURLs := remainingFailures(state.failedOrder, state.failed)

	return ParseResult{Books: state.books, FailedBuyURLs: failedBuyURLs}, nil
}

type parseState struct {
	seen               map[string]struct{}
	failed             map[string]struct{}
	recognizedListings map[string]struct{}
	books              []Book
	failedOrder        []string
}

func collectListing(state *parseState, node *html.Node, baseURL *url.URL) bool {
	book, buyURL, candidate, found := parseListing(node, baseURL)
	if !candidate {
		return false
	}
	if !found {
		if buyURL == "" {
			return true
		}
		if _, succeeded := state.recognizedListings[buyURL]; succeeded {
			return false
		}
		if _, alreadyFailed := state.failed[buyURL]; !alreadyFailed {
			state.failedOrder = append(state.failedOrder, buyURL)
			state.failed[buyURL] = struct{}{}
		}
		return false
	}

	state.recognizedListings[book.BuyURL] = struct{}{}
	delete(state.failed, book.BuyURL)
	if _, exists := state.seen[book.BuyURL]; exists {
		return false
	}
	state.seen[book.BuyURL] = struct{}{}
	state.books = append(state.books, book)

	return false
}

func remainingFailures(order []string, failed map[string]struct{}) []string {
	result := make([]string, 0, len(failed))
	for _, buyURL := range order {
		if _, stillFailed := failed[buyURL]; stillFailed {
			result = append(result, buyURL)
		}
	}

	return result
}

func parseListing(node *html.Node, baseURL *url.URL) (Book, string, bool, bool) {
	titleNode, _, buyNode := listingNodes(node)
	candidate := buyNode != nil || titleNode != nil && strings.Contains(normalizedText(node), priceLabel)
	if !candidate {
		return Book{}, "", false, false
	}

	buyURL := ""
	if buyNode != nil {
		buyURL = resolveURL(baseURL, href(buyNode))
	}
	book, found := parseBook(node, baseURL)
	if found {
		return book, book.BuyURL, true, true
	}

	return Book{}, buyURL, true, false
}

func isEmptySearchResultsPage(document *html.Node) bool {
	markers := emptySearchPageMarkers{}
	for node := range document.Descendants() {
		markers.record(node)
	}

	return markers.beginResultsFound && markers.emptyResultRegionFound &&
		markers.searchFormFound && markers.sortControlFound
}

func (markers *emptySearchPageMarkers) record(node *html.Node) {
	if node.Type != html.ElementNode {
		return
	}

	switch node.Data {
	case "a":
		if attribute(node, "name") == "beginStr" {
			markers.beginResultsFound = true
		}
	case "form":
		if isSearchForm(node) {
			markers.searchFormFound = true
		}
	case "p":
		if strings.HasPrefix(normalizedText(node), "Ссылка на этот поиск:") && hasEmptyResultRegion(node) {
			markers.emptyResultRegionFound = true
		}
	case "select":
		if attribute(node, "name") == "sortby" {
			markers.sortControlFound = true
		}
	}
}

func hasEmptyResultRegion(searchLink *html.Node) bool {
	firstDivider := nextElementSibling(searchLink)
	if firstDivider == nil || firstDivider.Data != "hr" {
		return false
	}
	secondDivider := nextElementSibling(firstDivider)
	if secondDivider == nil || secondDivider.Data != "hr" {
		return false
	}
	hint := nextElementSibling(secondDivider)

	return hint != nil && hint.Data == "p" && strings.Contains(normalizedText(hint), "Если ничего не найдено")
}

func nextElementSibling(node *html.Node) *html.Node {
	for sibling := node.NextSibling; sibling != nil; sibling = sibling.NextSibling {
		switch sibling.Type {
		case html.ElementNode:
			return sibling
		case html.TextNode:
			if strings.TrimSpace(sibling.Data) != "" {
				return nil
			}
		case html.CommentNode:
			continue
		case html.ErrorNode, html.DocumentNode, html.DoctypeNode, html.RawNode:
			return nil
		}
	}

	return nil
}

func isSearchForm(node *html.Node) bool {
	if attribute(node, "name") != "find3" {
		return false
	}

	action, err := url.Parse(attribute(node, "action"))

	return err == nil && strings.TrimPrefix(action.Path, "/") == "find3.php4"
}

func attribute(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}

	return ""
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
	if !positionBefore(titlePosition, buyPosition) || sellerFound &&
		(!positionBefore(titlePosition, sellerPosition) || !positionBefore(sellerPosition, buyPosition)) {
		return Book{}, false
	}
	bibliography := parseBibliography(lines, titlePosition, buyPosition, sellerPosition, sellerFound)
	content, condition := parseDescription(lines, buyPosition)
	seller, sellerURL, location := parseSeller(
		lines,
		sellerNode,
		sellerPosition,
		buyPosition,
		sellerFound,
		baseURL,
	)

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
		Photos:          photos(node, baseURL),
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

func positionBefore(left, right listingPosition) bool {
	return left.line < right.line || left.line == right.line && left.part < right.part
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
				end = sellerPosition.part
			} else {
				end = buyPosition.part
			}
		}
		if start >= end {
			continue
		}

		line := normalizedParts(lines[lineIndex][start:end])
		if sellerFound && lineIndex == sellerPosition.line {
			line = trimSellerPreamble(line)
		}
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
	sellerPosition, buyPosition listingPosition,
	sellerFound bool,
	baseURL *url.URL,
) (string, string, string) {
	if !sellerFound {
		return "", "", ""
	}

	line := lines[sellerPosition.line]
	locationEnd := len(line)
	if sellerPosition.line == buyPosition.line {
		locationEnd = buyPosition.part
	}
	location := normalizedParts(line[sellerPosition.part+1 : locationEnd])
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
		line, photoSectionFound := parseDescriptionLine(lines[lineIndex][start:])
		if line == "" {
			if photoSectionFound {
				break
			}
			continue
		}

		if conditionFound {
			conditionLines = append(conditionLines, line)
			if photoSectionFound {
				break
			}
			continue
		}

		beforeCondition, parsedCondition, found := splitCondition(line)
		if found {
			if beforeCondition != "" {
				contentLines = append(contentLines, beforeCondition)
			}
			conditionLines = append(conditionLines, parsedCondition)
			conditionFound = true
		} else {
			contentLines = append(contentLines, line)
		}
		if photoSectionFound {
			break
		}
	}

	return strings.Join(contentLines, "\n"), strings.Join(conditionLines, "\n")
}

func parseDescriptionLine(parts []listingPart) (string, bool) {
	photoSectionFound := false
	for partIndex, part := range parts {
		if isPhotoLink(part.node) {
			parts = parts[:partIndex]
			photoSectionFound = true
			break
		}
	}

	line := normalizedParts(parts)
	if beforePhotos, _, found := strings.Cut(line, photoLabel); found {
		line = beforePhotos
		photoSectionFound = true
	}

	return strings.TrimSpace(line), photoSectionFound
}

func splitCondition(line string) (string, string, bool) {
	beforeCondition, afterLabel, found := strings.Cut(line, conditionLabel)
	if !found {
		return "", "", false
	}

	condition := conditionLabel
	if afterLabel = strings.TrimSpace(afterLabel); afterLabel != "" {
		condition += " " + afterLabel
	}

	return strings.TrimSpace(beforeCondition), condition, true
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

func photos(node *html.Node, baseURL *url.URL) []Photo {
	var photos []Photo
	for descendant := range node.Descendants() {
		if isPhotoLink(descendant) {
			if photoURL := resolveURL(baseURL, href(descendant)); photoURL != "" {
				caption := normalizedText(descendant)
				if caption == "" {
					caption = "фото"
				}
				photos = append(photos, Photo{URL: photoURL, Caption: caption})
			}
		}
	}

	return photos
}

func isPhotoLink(node *html.Node) bool {
	return node != nil && node.Type == html.ElementNode && node.Data == "a" &&
		strings.Contains(href(node), "foto.php4")
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
	if strings.TrimSpace(raw) == "" {
		return ""
	}

	reference, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	return baseURL.ResolveReference(reference).String()
}
