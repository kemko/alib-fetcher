// Package alib fetches and parses book listings from Alib.ru.
package alib

// Book is one sale listing from Alib.ru.
type Book struct {
	Title     string
	Seller    string
	Price     string
	Condition string
	BuyURL    string
}
