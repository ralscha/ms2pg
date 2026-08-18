package sqlrewrite

import "testing"

func TestProtectRestoresStringsAndComments(t *testing.T) {
	input := "N'[name] GETDATE()' + value -- TOP GETDATE()\n/* [comment] */"
	protected := Protect(input, true)
	rewritten := protected.SQL + " suffix"
	want := "'[name] GETDATE()' + value -- TOP GETDATE()\n/* [comment] */ suffix"
	if got := protected.Restore(rewritten); got != want {
		t.Fatalf("Restore() = %q, want %q", got, want)
	}
}

func TestProtectHandlesEscapedQuotes(t *testing.T) {
	input := `N'it''s [literal]'`
	protected := Protect(input, true)
	if got, want := protected.Restore(protected.SQL), `'it''s [literal]'`; got != want {
		t.Fatalf("Restore() = %q, want %q", got, want)
	}
}

func TestProtectHandlesNestedBlockComments(t *testing.T) {
	input := `value /* outer /* [inner] */ GETDATE() */ + [column]`
	protected := Protect(input, false)
	if got := protected.Restore(protected.SQL); got != input {
		t.Fatalf("Restore() = %q, want %q", got, input)
	}
}
