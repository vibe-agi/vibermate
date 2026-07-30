package locales_test

import (
	"reflect"
	"testing"

	"github.com/vibe-agi/vibermate/locales"
)

func TestProductionCatalogsExposeIdenticalNonemptyKeys(t *testing.T) {
	t.Parallel()

	catalogs, err := locales.New()
	if err != nil {
		t.Fatal(err)
	}
	english := catalogs.Keys(locales.EnglishUS)
	chinese := catalogs.Keys(locales.SimplifiedChinese)
	if !reflect.DeepEqual(english, chinese) || len(english) == 0 {
		t.Fatalf("catalog keys en=%v zh=%v", english, chinese)
	}
	for _, locale := range []locales.Locale{
		locales.EnglishUS,
		locales.SimplifiedChinese,
	} {
		for _, key := range english {
			names, err := catalogs.Parameters(key)
			if err != nil {
				t.Fatal(err)
			}
			parameters := make(map[string]string, len(names))
			for _, name := range names {
				parameters[name] = "value"
			}
			value, err := catalogs.Render(locale, key, parameters)
			if err != nil || value == "" {
				t.Fatalf("Render(%s, %s) value=%q error=%v", locale, key, value, err)
			}
		}
	}
}

func TestLocaleDetectionUsesOnlySupportedCatalogs(t *testing.T) {
	t.Parallel()

	if got := locales.Detect([]string{"LANG=zh_CN.UTF-8"}); got != locales.SimplifiedChinese {
		t.Fatalf("Detect(zh_CN) = %s", got)
	}
	if got := locales.Detect([]string{"LANG=fr_FR.UTF-8"}); got != locales.EnglishUS {
		t.Fatalf("Detect(fr_FR) = %s", got)
	}
}
