// Package alib fetches and parses book listings from Alib.ru.
package alib

import (
	"encoding/json"
	"strings"
)

// Book is one sale listing from Alib.ru.
type Book struct {
	Title        string `json:"title"`
	Bibliography string `json:"bibliography,omitempty"`
	Content      string `json:"content,omitempty"`
	Seller       string `json:"seller"`
	SellerURL    string `json:"seller_url"`
	Location     string `json:"location,omitempty"`
	Price        string `json:"price,omitempty"`
	Condition    string `json:"condition,omitempty"`
	BuyURL       string `json:"buy_url"`

	// Deprecated fragment fields exist only while decoded legacy records remain in memory.
	TextBeforeSeller string `json:"-"`
	TextBeforeBuy    string `json:"-"`
	TextAfterBuy     string `json:"-"`
	PublicationYear  int    `json:"publication_year,omitempty"`
	HasPhotos        bool   `json:"has_photos"`
}

type legacyBookJSON struct {
	TextBeforeSeller *string `json:"text_before_seller"`
	TextBeforeBuy    *string `json:"text_before_buy"`
	TextAfterBuy     *string `json:"text_after_buy"`
}

// UnmarshalJSON decodes semantic books and converts persisted legacy text fragments.
func (b *Book) UnmarshalJSON(data []byte) error {
	type semanticBookJSON Book

	var semantic semanticBookJSON
	if err := json.Unmarshal(data, &semantic); err != nil {
		return err
	}

	var legacy legacyBookJSON
	if err := json.Unmarshal(data, &legacy); err != nil {
		return err
	}

	*b = Book(semantic)
	if legacy.TextBeforeSeller == nil && legacy.TextBeforeBuy == nil && legacy.TextAfterBuy == nil {
		return nil
	}

	b.TextBeforeSeller = legacyString(legacy.TextBeforeSeller)
	b.TextBeforeBuy = legacyString(legacy.TextBeforeBuy)
	b.TextAfterBuy = legacyString(legacy.TextAfterBuy)
	b.convertLegacyFragments()

	return nil
}

func (b *Book) convertLegacyFragments() {
	hasSeller := b.Seller != "" || b.SellerURL != ""
	b.Bibliography = legacyBibliography(b.TextBeforeSeller, hasSeller)
	b.PublicationYear = parsePublicationYear(b.Bibliography)
	b.Seller = strings.TrimPrefix(b.Seller, sellerPrefix)
	b.Location, b.Price = legacySaleDetails(b.TextBeforeSeller, b.TextBeforeBuy, hasSeller)
	b.Content, b.Condition = legacyDescription(b.TextAfterBuy)
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

	lastLineBreak := strings.LastIndex(text, "\n")
	if lastLineBreak < 0 {
		return ""
	}

	return strings.TrimSpace(text[:lastLineBreak])
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
