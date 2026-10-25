package flags

import (
	"reflect"
	"testing"
)

// TestAllNilSeesEveryPatchField calls allNil directly with exactly one Patch
// field set, for every field of Patch.
//
// Failure prevented: allNil is only reached through EnvProvider, and
// EnvProvider deliberately maps no env var onto server-evaluated gates such as
// BulletinEnabled and GitHubMirrorEnabled — so a field missing from allNil
// changes nothing observable today, and the external reflection test never
// calls it at all. The omission only bites the day someone wires an env var
// for that field: EnvProvider would then return "no opinion" and silently drop
// it. This test makes the "also update allNil()" note on Patch enforceable.
func TestAllNilSeesEveryPatchField(t *testing.T) {
	t.Parallel()

	if !allNil(&Patch{}) {
		t.Fatal("allNil(&Patch{}) = false, want true for a patch with no opinions")
	}

	patchType := reflect.TypeOf(Patch{})
	for i := range patchType.NumField() {
		field := patchType.Field(i)
		t.Run(field.Name, func(t *testing.T) {
			t.Parallel()

			if field.Type.Kind() != reflect.Pointer {
				t.Fatalf("Patch field %s is %v, want a pointer so nil can mean no opinion", field.Name, field.Type)
			}
			var p Patch
			// a non-nil pointer to the zero value is still an opinion
			reflect.ValueOf(&p).Elem().Field(i).Set(reflect.New(field.Type.Elem()))

			if allNil(&p) {
				t.Errorf("allNil reported no opinions for a patch with only %s set; add it to allNil in env.go", field.Name)
			}
		})
	}
}
