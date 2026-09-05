// Package testutil provides shared helpers for repository tests.
package testutil

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	xhtml "golang.org/x/net/html"

	"github.com/kemko/alib-fetcher/internal/alib"
	"github.com/kemko/alib-fetcher/internal/digest"
)

// RenderChunks renders a digest and requires that no books were skipped.
func RenderChunks(t *testing.T, books []alib.Book, options digest.Options) ([]digest.Chunk, error) {
	t.Helper()

	chunks, skippedBuyURLs, err := digest.RenderSendable(books, options, 0)
	require.Empty(t, skippedBuyURLs)

	return chunks, err
}

// DisplayedRuneCount counts displayed HTML runes independently of the renderer.
func DisplayedRuneCount(t *testing.T, value string) int {
	t.Helper()

	document, err := xhtml.Parse(strings.NewReader(value))
	require.NoError(t, err)

	var count func(*xhtml.Node) int
	count = func(node *xhtml.Node) int {
		total := 0
		if node.Type == xhtml.TextNode {
			total += len([]rune(node.Data))
		}
		if node.Type == xhtml.ElementNode && node.Data == "br" {
			total++
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			total += count(child)
		}

		return total
	}

	return count(document)
}

// ListingPage builds a minimal Alib listing for test inputs.
func ListingPage(title, buyURL, price string) string {
	return "<p><b>" + title + "</b> Цена: " + price + " <a href=\"" + buyURL + "\"><b>Купить</b></a></p>"
}
