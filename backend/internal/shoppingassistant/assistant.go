// Package shoppingassistant implements deterministic catalog-grounded demos only.
// It has no identity, cart, order, persistence or mutation dependencies.
package shoppingassistant

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/aiprotocol"
	"github.com/WilliamSchweitzer/cartside/backend/internal/catalog"
)

type Catalog interface {
	List(context.Context, catalog.Query) (catalog.Page, error)
	Get(context.Context, string) (catalog.Product, error)
}

// Demo search terms are explicit teaching fixtures, not language understanding or
// verified dietary classification. Search relevance does not establish suitability.
var productIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var demoTerms = map[string][]string{
	"breakfast for two":   {"oats", "milk", "bananas"},
	"vegetarian dinner":   {"rice", "beans", "broccoli"},
	"snacks for a picnic": {"apples", "crackers", "cheese"},
}

type Suggestion struct {
	ProductID  string `json:"product_id"`
	Quantity   int    `json:"quantity"`
	Reason     string `json:"reason"`
	Name       string `json:"name"`
	Package    string `json:"package_label"`
	PriceCents int32  `json:"price_cents"`
	Currency   string `json:"currency"`
	Available  bool   `json:"available"`
}
type Result struct {
	Version     int          `json:"version"`
	Provider    string       `json:"provider"`
	Text        string       `json:"text"`
	Suggestions []Suggestion `json:"suggestions"`
}
type Assistant struct {
	catalog Catalog
	client  *Client
}

func New(c Catalog, client *Client) *Assistant { return &Assistant{c, client} }
func (a *Assistant) Suggest(ctx context.Context, request string) (Result, error) {
	result := Result{Version: 1, Provider: "simulated: cartside-mock-v1", Suggestions: []Suggestion{}}
	if !bounded(request, 256) {
		return Result{}, failure(InvalidInput)
	}
	scenario := strings.ToLower(strings.Join(strings.Fields(request), " "))
	terms, ok := demoTerms[scenario]
	if !ok {
		result.Text = "Choose breakfast for two, vegetarian dinner, or snacks for a picnic. Dietary and allergen suitability is unverified."
		return result, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	input := aiprotocol.Input{Version: 1, Scenario: scenario, Candidates: []aiprotocol.Candidate{}}
	seen := map[string]bool{}
	for _, term := range terms {
		page, e := a.catalog.List(ctx, catalog.Query{Search: term, Limit: 2})
		if e != nil {
			return Result{}, failure(CatalogFailure)
		}
		for i, p := range page.Items {
			if i >= 2 {
				break
			}
			if !p.Available || seen[p.ID] {
				continue
			}
			if !productIDPattern.MatchString(p.ID) || !bounded(p.Name, 160) || !bounded(p.PackageLabel, 100) {
				return Result{}, failure(CatalogFailure)
			}
			seen[p.ID] = true
			input.Candidates = append(input.Candidates, aiprotocol.Candidate{ProductID: p.ID, Name: p.Name, Package: p.PackageLabel})
		}
	}
	if len(input.Candidates) == 0 {
		result.Text = "No available demo candidates were found. Please try another scenario."
		return result, nil
	}
	out, e := a.client.Complete(ctx, input)
	if e != nil {
		return Result{}, e
	}
	for _, s := range out.Suggestions {
		p, e := a.catalog.Get(ctx, s.ProductID)
		if e != nil {
			if errors.Is(e, catalog.ErrNotFound) {
				return Result{}, failure(CatalogChanged)
			}
			return Result{}, failure(CatalogFailure)
		}
		if p.ID != s.ProductID || !p.Available {
			return Result{}, failure(CatalogChanged)
		}
		result.Suggestions = append(result.Suggestions, Suggestion{ProductID: p.ID, Quantity: s.Quantity, Reason: s.Reason, Name: p.Name, Package: p.PackageLabel, PriceCents: p.PriceCents, Currency: p.Currency, Available: p.Available})
	}
	result.Text = out.Text
	return result, nil
}
