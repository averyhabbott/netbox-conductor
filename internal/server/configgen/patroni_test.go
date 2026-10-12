package configgen

import (
	"strings"
	"testing"
)

func TestRenderPatroniQuotesPasswords(t *testing.T) {
	out, err := RenderPatroni(PatroniInput{
		Scope:       "c1",
		NodeName:    "n1",
		NodeAddr:    "10.0.0.1",
		DBSuperPass: `!a: b #c"\`,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `password: "!a: b #c\"\\"`
	if !strings.Contains(out, want) {
		t.Fatalf("superuser password not quoted; want line containing %s in:\n%s", want, out)
	}
}
