package sessionstream

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateBatch(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		reason string
		line   int
	}{
		{"ok", "{\"a\":1}\n{\"b\":2}\n", "", 0},
		{"blank lines allowed", "{\"a\":1}\n\n{\"b\":2}\n", "", 0},
		{"empty", "", ReasonEmptyBody, 0},
		{"torn tail", "{\"a\":1}\n{\"b\":", ReasonTornTail, 2},
		{"array line", "{\"a\":1}\n[1]\n", ReasonNotNDJSON, 2},
		{"invalid json", "{\"a\":1}\n{nope}\n", ReasonNotNDJSON, 2},
		{"bad utf8", "{\"a\":\"\xff\"}\n", ReasonNotNDJSON, 0},
		{"too large", strings.Repeat("{\"a\":1}\n", MaxBatchBytes/8+1), ReasonTooLarge, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBatch([]byte(tc.body))
			if tc.reason == "" {
				if err != nil {
					t.Fatalf("valid batch refused: %v", err)
				}
				return
			}
			var be *BatchError
			if !errors.As(err, &be) || be.Reason != tc.reason || be.Line != tc.line {
				t.Fatalf("got %v, want reason %s line %d", err, tc.reason, tc.line)
			}
		})
	}
}

func TestKeysAreTeamScopedAndOrdered(t *testing.T) {
	const team, cnv, layer = "team_xlcr6yzpec", "cnv_01a1ecc3-a4c4-76c6-8226-b3dba9d29122", "clyr_01a1ecc3-a4c4-76c6-8226-b3dba9d29123"
	k1, err := BytesKey(team, cnv, layer, 9)
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := BytesKey(team, cnv, layer, 10)
	if k1 >= k2 {
		t.Fatalf("keys must sort in offset order: %s !< %s", k1, k2)
	}
	if !strings.HasPrefix(k1, "team/"+team+"/conversations/"+cnv+"/layers/session-jsonl."+layer+"/bytes/") {
		t.Fatalf("unexpected layout: %s", k1)
	}
	if off, ok := ParseBytesKeyOffset(k2); !ok || off != 10 {
		t.Fatalf("parse offset: %d %v", off, ok)
	}
	for name, args := range map[string][3]string{
		"traversal team":   {"team_../../x", cnv, layer},
		"not uuidv7 cnv":   {team, "cnv_01a1ecc3-a4c4-46c6-8226-b3dba9d29122", layer},
		"layer is a path":  {team, cnv, "clyr_../x"},
		"missing prefixes": {"xlcr6yzpec", cnv, layer},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := BytesKey(args[0], args[1], args[2], 0); err == nil {
				t.Fatal("invalid id accepted into an object key")
			}
		})
	}
	if _, err := AttachmentKey(team, cnv, "ABC"); err == nil {
		t.Fatal("non-hex attachment digest accepted")
	}
}

func TestCheckContiguous(t *testing.T) {
	if n, err := CheckContiguous([]IndexObject{{0, 5, ""}, {5, 3, ""}}); err != nil || n != 8 {
		t.Fatalf("contiguous refused: %d %v", n, err)
	}
	if _, err := CheckContiguous([]IndexObject{{0, 5, ""}, {6, 3, ""}}); err == nil {
		t.Fatal("gap accepted")
	}
	if _, err := CheckContiguous([]IndexObject{{0, 5, ""}, {4, 3, ""}}); err == nil {
		t.Fatal("overlap accepted")
	}
}
