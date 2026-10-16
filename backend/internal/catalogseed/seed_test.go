package catalogseed

import (
	"strings"
	"testing"
)

func TestFixture(t *testing.T) {
	products, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	categories := map[string]bool{}
	for _, p := range products {
		categories[p.Category] = true
	}
	if len(categories) != 6 {
		t.Fatalf("categories: %v", categories)
	}
}
func TestValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]Product) []Product
	}{
		{"count", func(p []Product) []Product { return p[:99] }},
		{"duplicate", func(p []Product) []Product { p[99].ID = p[0].ID; return p }},
		{"invalid UUID", func(p []Product) []Product { p[0].ID = "bad"; return p }},
		{"zero UUID", func(p []Product) []Product { p[0].ID = "00000000-0000-0000-0000-000000000000"; return p }},
		{"name", func(p []Product) []Product { p[0].Name = " \t"; return p }},
		{"description", func(p []Product) []Product { p[0].Description = ""; return p }},
		{"package", func(p []Product) []Product { p[0].PackageLabel = ""; return p }},
		{"image", func(p []Product) []Product { p[0].ImageReference = ""; return p }},
		{"category", func(p []Product) []Product { p[0].Category = "other"; return p }},
		{"currency", func(p []Product) []Product { p[0].Currency = "EUR"; return p }},
		{"missing price", func(p []Product) []Product { p[0].PriceCents = nil; return p }},
		{"negative price", func(p []Product) []Product { v := int64(-1); p[0].PriceCents = &v; return p }},
		{"overflow price", func(p []Product) []Product { v := int64(2147483648); p[0].PriceCents = &v; return p }},
		{"missing availability", func(p []Product) []Product { p[0].Available = nil; return p }},
		{"unavailable", func(p []Product) []Product { v := false; p[0].Available = &v; return p }},
		{"nul text", func(p []Product) []Product { p[0].Name = "bad\x00"; return p }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if Validate(tc.change(p)) == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	p, _ := Load()
	zero := int64(0)
	p[0].PriceCents = &zero
	if err := Validate(p); err != nil {
		t.Fatal(err)
	}
}
func TestDecodeRejectsMalformedInput(t *testing.T) {
	for _, input := range []string{"[", fixture + " {}", strings.Replace(fixture, `"name":`, `"unknown":`, 1), strings.Replace(fixture, `"price_cents": 499`, `"price_cents": 1.5`, 1)} {
		if _, err := Decode(strings.NewReader(input)); err == nil {
			t.Fatal("expected decode failure")
		}
	}
}
