package aiprotocol

import "testing"

func TestStrictFields(t *testing.T) {
	for _, s := range []string{`{"Version":1,"text":"x","suggestions":[]}`, `{"version":1,"text":"x"}`, `{"version":1,"text":"x","suggestions":null}`, `{"version":1,"text":"x","suggestions":[],"extra":1}`, `{"version":1,"text":"x","text":"y","suggestions":[]}`, `{"version":1,"text":"x","suggestions":[]} {}`, `{"version":1,"text":"x","suggestions":[{"product_id":"x","quantity":1,"reason":"x","reason":"y"}]}`} {
		var o Output
		if Strict([]byte(s), &o) == nil {
			t.Fatalf("accepted invalid content %s", s)
		}
	}
	var o Output
	if err := Strict([]byte(`{"version":1,"text":"x","suggestions":[]}`), &o); err != nil {
		t.Fatal(err)
	}
}
