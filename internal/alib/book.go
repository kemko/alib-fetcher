// Package alib fetches and parses book listings from Alib.ru.
package alib

import (
	"encoding/json"
	"strings"
)

// Book is one sale listing from Alib.ru.
type Book struct {
	Title           string `json:"title"`
	Bibliography    string `json:"bibliography,omitempty"`
	Content         string `json:"content,omitempty"`
	Seller          string `json:"seller"`
	SellerURL       string `json:"seller_url"`
	Location        string `json:"location,omitempty"`
	Price           string `json:"price,omitempty"`
	Condition       string `json:"condition,omitempty"`
	BuyURL          string `json:"buy_url"`
	PublicationYear int    `json:"publication_year,omitempty"`
	HasPhotos       bool   `json:"has_photos"`
}

type legacyBookJSON struct {
	TextBeforeSeller *string `json:"text_before_seller"`
	TextBeforeBuy    *string `json:"text_before_buy"`
	TextAfterBuy     *string `json:"text_after_buy"`
}

// UnmarshalJSON decodes semantic books and converts persisted legacy text fragments.
func (b *Book) UnmarshalJSON(data []byte) error {
	type semanticBookJSON Book

	decoded := struct {
		legacyBookJSON
		semanticBookJSON
	}{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	*b = Book(decoded.semanticBookJSON)
	if decoded.TextBeforeSeller == nil && decoded.TextBeforeBuy == nil && decoded.TextAfterBuy == nil {
		return nil
	}

	b.convertLegacyFragments(
		legacyString(decoded.TextBeforeSeller),
		legacyString(decoded.TextBeforeBuy),
		legacyString(decoded.TextAfterBuy),
	)

	return nil
}

func (b *Book) convertLegacyFragments(textBeforeSeller, textBeforeBuy, textAfterBuy string) {
	hasSeller := b.Seller != "" || b.SellerURL != ""
	b.Bibliography = legacyBibliography(textBeforeSeller, hasSeller)
	b.PublicationYear = parsePublicationYear(b.Bibliography)
	b.Seller = strings.TrimPrefix(b.Seller, sellerPrefix)
	b.Location, b.Price = legacySaleDetails(textBeforeSeller, textBeforeBuy, hasSeller)
	b.Content, b.Condition = legacyDescription(textAfterBuy)
}

func legacyString(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}

func legacyBibliography(text string, hasSeller bool) string {
	if !hasSeller {
		beforePrice, _, _ := strings.Cut(text, priceLabel)

		return strings.TrimSpace(beforePrice)
	}

	return trimSellerPreamble(text)
}

func trimSellerPreamble(text string) string {
	const sellerWord = "продавца"

	sellerWordStart := strings.LastIndex(text, sellerWord)
	if sellerWordStart >= 0 {
		if preambleStart := strings.LastIndex(text[:sellerWordStart], "("); preambleStart >= 0 {
			return strings.TrimSpace(text[:preambleStart])
		}
	}

	return strings.TrimSpace(text)
}

func legacySaleDetails(textBeforeSeller, textBeforeBuy string, hasSeller bool) (string, string) {
	saleText := textBeforeBuy
	if !hasSeller {
		saleText = textBeforeSeller
	}

	beforePrice, price, found := strings.Cut(saleText, priceLabel)
	if !found {
		beforePrice = saleText
		price = ""
	}

	location := ""
	if hasSeller {
		location = cleanLocation(beforePrice)
	}

	return location, strings.TrimSpace(price)
}

func legacyDescription(text string) (string, string) {
	text = strings.TrimSpace(text)
	content, condition, found := strings.Cut(text, conditionLabel)
	if !found {
		return text, ""
	}

	return strings.TrimSpace(content), strings.TrimSpace(conditionLabel + condition)
}
