package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Every Ukrainian string in the program must be in the catalog, either as a
// message of its own or as a piece of one (strings glued together in code).
func TestCatalogCoversCode(t *testing.T) {
	keys := Keys()
	covered := func(s string) bool {
		if Known(s) {
			return true
		}
		for _, k := range keys {
			if strings.Contains(k, s) {
				return true
			}
		}
		return false
	}
	var missing []string
	root := "../.."
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			// PowerShell scripts: their own messages are in the catalog separately.
			if err != nil || !cyr.MatchString(s) || strings.Contains(s, "$env:") || strings.HasSuffix(p, "i18n.go") {
				return true
			}
			if !covered(s) {
				missing = append(missing, fset.Position(lit.Pos()).String()+": "+strconv.Quote(s))
			}
			return true
		})
		return nil
	})
	if len(missing) > 0 {
		t.Fatalf("%d рядків без перекладу:\n%s", len(missing), strings.Join(missing, "\n"))
	}
}

// English formats keep the same number of values as the Ukrainian ones.
func TestCatalogVerbs(t *testing.T) {
	var m map[string]string
	if err := jsonUnmarshal(&m); err != nil {
		t.Fatal(err)
	}
	for uk, en := range m {
		a, b := len(verb.FindAllString(uk, -1)), len(verb.FindAllString(en, -1))
		if a != b {
			t.Errorf("%q: %d значень, у перекладі %d", uk, a, b)
		}
		if cyr.MatchString(en) {
			t.Errorf("%q: у перекладі лишилась кирилиця: %q", uk, en)
		}
	}
}

func TestTranslate(t *testing.T) {
	cases := map[string]string{
		"точка «Вокзальна» підключилась з 198.51.100.24:41000 через WireGuard (KASA-01, 3 принтер(и))":  "shop “Вокзальна” connected from 198.51.100.24:41000 over WireGuard (KASA-01, 3 printer(s))",
		"зв'язок з сервером: сервер відмовив: невідомий код підключення":                                "connection to the server: the server refused: unknown connection code",
		"завдання X1 → «Каса»: 812 байт, передано в чергу Windows (службова черга «SlipDrukka (Каса)»)": "job X1 → “Каса”: 812 bytes, handed to the Windows queue (service queue “SlipDrukka (Каса)”)",
		"невідомий код підключення": "unknown connection code",
		"Вокзальна":       "Вокзальна",
		"already English": "already English",
	}
	for in, want := range cases {
		if got := Tr(EN, in); got != want {
			t.Errorf("\n in: %s\ngot: %s\nwant: %s", in, got, want)
		}
	}
	if Tr(UK, "невідомий код підключення") != "невідомий код підключення" {
		t.Error("українською має лишатися як є")
	}
}
