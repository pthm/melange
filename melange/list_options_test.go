package melange

import (
	"errors"
	"testing"
)

func TestWithObjectFilter_Encoding(t *testing.T) {
	tests := []struct {
		name     string
		relation Relation
		subject  Object
		want     string
	}{
		{"plain", "workspace", Object{Type: "workspace", ID: "7"}, "workspace@workspace:7"},
		// The SQL side splits the subject on its FIRST colon, which is why the
		// generated parse uses substring/position rather than split_part. An ID
		// carrying colons or an '@' must survive the round trip intact.
		{"colons in id", "tenant", Object{Type: "tenant", ID: "eu:west:1"}, "tenant@tenant:eu:west:1"},
		{"email as id", "account", Object{Type: "user", ID: "alice@example.com"}, "account@user:alice@example.com"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, err := applyListObjects([]ListObjectsOption{WithObjectFilter(tc.relation, tc.subject)})
			if err != nil {
				t.Fatalf("applyListObjects: %v", err)
			}
			if o.filter != tc.want {
				t.Errorf("filter = %q, want %q", o.filter, tc.want)
			}
			if o.param() != tc.want {
				t.Errorf("param() = %v, want %q", o.param(), tc.want)
			}
		})
	}
}

// No filter must bind SQL NULL, not the empty string — NULL is what disables
// filtering in the generated function.
func TestWithObjectFilter_AbsentBindsNull(t *testing.T) {
	o, err := applyListObjects(nil)
	if err != nil {
		t.Fatalf("applyListObjects: %v", err)
	}
	if o.param() != nil {
		t.Errorf("param() = %v, want nil", o.param())
	}
}

// A delimiter inside a part would re-parse server-side into a different
// relation or subject than the caller wrote — a mis-scoped list rather than an
// error, which is the failure this guard exists to prevent.
func TestValidateObjectFilter_RejectsDelimiters(t *testing.T) {
	tests := []struct {
		name     string
		relation Relation
		subject  Object
	}{
		{"at in relation", "a@b", Object{Type: "workspace", ID: "7"}},
		{"colon in relation", "a:b", Object{Type: "workspace", ID: "7"}},
		{"hash in relation", "a#b", Object{Type: "workspace", ID: "7"}},
		{"at in subject type", "workspace", Object{Type: "a@b", ID: "7"}},
		{"colon in subject type", "workspace", Object{Type: "a:b", ID: "7"}},
		{"hash in subject type", "workspace", Object{Type: "a#b", ID: "7"}},
		{"userset subject", "workspace", Object{Type: "workspace", ID: "7#view"}},
		{"empty relation", "", Object{Type: "workspace", ID: "7"}},
		{"empty subject id", "workspace", Object{Type: "workspace", ID: ""}},
		{"empty subject type", "workspace", Object{Type: "", ID: "7"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := applyListObjects([]ListObjectsOption{WithObjectFilter(tc.relation, tc.subject)})
			if !errors.Is(err, ErrInvalidObjectFilter) {
				t.Errorf("err = %v, want ErrInvalidObjectFilter", err)
			}
		})
	}
}

// A colon in the subject type is the subtle one, and the reason validation runs
// on the parts rather than the encoded string: encoding is lossy here. A type
// of "a:b" with id "c" encodes to exactly the same string as the legitimate
// type "a" with id "b:c", so after encoding nothing can tell them apart.
func TestValidateObjectFilter_ColonInTypeIsCaughtBeforeEncoding(t *testing.T) {
	_, err := applyListObjects([]ListObjectsOption{
		WithObjectFilter(Relation("rel"), Object{Type: "a:b", ID: "c"}),
	})
	if !errors.Is(err, ErrInvalidObjectFilter) {
		t.Fatalf("err = %v, want ErrInvalidObjectFilter", err)
	}

	// The collision this prevents: the legitimate filter below encodes to the
	// same string the rejected one would have.
	ok, err := applyListObjects([]ListObjectsOption{
		WithObjectFilter(Relation("rel"), Object{Type: "a", ID: "b:c"}),
	})
	if err != nil {
		t.Fatalf("colons in an id are legal: %v", err)
	}
	if ok.filter != "rel@a:b:c" {
		t.Fatalf("filter = %q, want %q", ok.filter, "rel@a:b:c")
	}
}
