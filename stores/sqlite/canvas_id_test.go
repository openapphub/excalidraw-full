package sqlite

import "testing"

func TestIsIndexedDBCanvasID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id   string
		want bool
	}{
		{"a1b2c3d4-e5f6-7890-abcd-ef1234567890", true},
		{"A1B2C3D4-E5F6-7890-ABCD-EF1234567890", true},
		{"01M00HGJRY08R6MS61XTP5SKK4", false},
		{"lhUcOj8CoiRiH41S2ijx-", false},
		{"zMrydH-TaKbY-lzhFmcQG", false},
		{"ai-01KZXRB7GQS7GJKA5S5EEBVVQ0", false},
		{"ai-canvas", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isIndexedDBCanvasID(tc.id); got != tc.want {
			t.Fatalf("isIndexedDBCanvasID(%q) = %v，期望 %v", tc.id, got, tc.want)
		}
	}
}
