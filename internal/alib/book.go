// Package alib fetches and parses book listings from Alib.ru.
package alib

// Book is one sale listing from Alib.ru.
type Book struct {
	Title            string
	TextBeforeSeller string
	Seller           string
	SellerURL        string
	TextBeforeBuy    string
	BuyURL           string
	TextAfterBuy     string
	HasPhotos        bool
}
