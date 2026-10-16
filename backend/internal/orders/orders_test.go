package orders

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
)

const apple = "00000000-0000-0000-0000-000000000001"
const pear = "00000000-0000-0000-0000-000000000002"

func TestCanonical(t *testing.T) {
	in := CheckoutInput{"key", 4, []ReviewedItem{{pear, 2, 30}, {apple, 1, 20}}}
	got, a, err := canonical(in)
	if err != nil {
		t.Fatal(err)
	}
	if in.Items[0].ProductID != pear {
		t.Fatal("mutated caller input")
	}
	_, b, err := canonical(CheckoutInput{"key", 4, []ReviewedItem{{apple, 1, 20}, {pear, 2, 30}}})
	if err != nil || !reflect.DeepEqual(a, b) || got.Items[0].ProductID != apple {
		t.Fatal("canonical order")
	}
	for _, bad := range []CheckoutInput{{"", 4, in.Items}, {"has space", 4, in.Items}, {"key", -1, in.Items}, {"key", 4, nil}, {"key", 4, []ReviewedItem{{apple, 1, 20}, {apple, 2, 20}}}, {"key", 4, []ReviewedItem{{"bad", 1, 20}}}, {"key", 4, []ReviewedItem{{apple, 100, 20}}}, {"key", 4, []ReviewedItem{{apple, 1, -1}}}} {
		if _, _, err := canonical(bad); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("accepted %+v", bad)
		}
	}

	lower := CheckoutInput{"key", 1, []ReviewedItem{{"abcdef01-0000-0000-0000-000000000001", 1, 0}}}
	upper := CheckoutInput{"key", 1, []ReviewedItem{{"ABCDEF01-0000-0000-0000-000000000001", 1, 0}}}
	_, lf, _ := canonical(lower)
	_, uf, _ := canonical(upper)
	if !reflect.DeepEqual(lf, uf) {
		t.Fatal("UUID casing changed fingerprint")
	}
	upper.Items = append(upper.Items, lower.Items[0])
	if _, _, err := canonical(upper); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("canonical duplicate accepted")
	}
	for _, changed := range []CheckoutInput{{"key", 5, in.Items}, {"key", 4, []ReviewedItem{{apple, 2, 20}, {pear, 2, 30}}}, {"key", 4, []ReviewedItem{{apple, 1, 21}, {pear, 2, 30}}}} {
		_, fp, err := canonical(changed)
		if err != nil || reflect.DeepEqual(a, fp) {
			t.Fatal("fingerprint missed precondition")
		}
	}
	if _, err := (&Service{}).Checkout(context.Background(), in); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
}
func TestTotalOverflow(t *testing.T) {
	for _, tc := range []struct {
		total, price int64
		q            int
	}{{0, math.MaxInt64, 2}, {math.MaxInt64, 1, 1}, {0, -1, 1}} {
		if _, _, err := addTotal(tc.total, tc.price, tc.q); !errors.Is(err, ErrOverflow) {
			t.Fatal(tc, err)
		}
	}
	total, line, err := addTotal(1, math.MaxInt32, 99)
	if err != nil || line != int64(math.MaxInt32)*99 || total != line+1 {
		t.Fatal(total, line, err)
	}
}
