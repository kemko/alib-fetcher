// Package alib fetches and parses book listings from Alib.ru.
package alib

// Book is one sale listing from Alib.ru.
type Book struct {
	Title            string `json:"title"`
	TextBeforeSeller string `json:"text_before_seller"`
	Seller           string `json:"seller"`
	SellerURL        string `json:"seller_url"`
	TextBeforeBuy    string `json:"text_before_buy"`
	BuyURL           string `json:"buy_url"`
	TextAfterBuy     string `json:"text_after_buy"`
	HasPhotos        bool   `json:"has_photos"`
}
