package secrets

import "testing"

func TestRoundTrip(t *testing.T) {
	box, err := New("test-key")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal([]byte(`{"token":"abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := box.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"token":"abc"}` {
		t.Fatalf("got %q", got)
	}

	other, _ := New("other-key")
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("expected decrypt failure with a different key")
	}
}
