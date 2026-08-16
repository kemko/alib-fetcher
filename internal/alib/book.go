// Package alib fetches and parses book listings from Alib.ru.
package alib

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

	// Deprecated fragment fields remain temporarily for in-process rendering compatibility.
	TextBeforeSeller string `json:"text_before_seller,omitempty"`
	TextBeforeBuy    string `json:"text_before_buy,omitempty"`
	TextAfterBuy     string `json:"text_after_buy,omitempty"`
	PublicationYear  int    `json:"publication_year,omitempty"`
	HasPhotos        bool   `json:"has_photos"`
}
