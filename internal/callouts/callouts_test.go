package callouts

import "testing"

func TestNormalizeAncientCallouts(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"B点/B坡", "B坡"},
		{"B点/B坡×2", "B坡×2"},
		{"A点/A大房", "A大房"},
		{"a main", "A大房"},
		{"cave", "洞穴"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := Normalize("de_ancient", c.in); got != c.want {
				t.Fatalf("Normalize(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestNormalizeTextUsesCurrentMapAliases(t *testing.T) {
	got := NormalizeText("de_mirage", "你在 apps 没跟上，后续又去 connector 补位")
	want := "你在 B公寓 没跟上，后续又去 拱门 补位"
	if got != want {
		t.Fatalf("NormalizeText() = %q, want %q", got, want)
	}
}
