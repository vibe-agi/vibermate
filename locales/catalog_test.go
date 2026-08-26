package locales_test

import (
	"testing"

	"github.com/vibe-agi/vibermate/locales"
)

func TestProductionCatalogsRenderCLIMessage(t *testing.T) {
	t.Parallel()

	catalogs, err := locales.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, locale := range []locales.Locale{
		locales.EnglishUS,
		locales.SimplifiedChinese,
	} {
		value, err := catalogs.Render(locale, "cli.error.launchFailed", nil)
		if err != nil || value == "" {
			t.Fatalf("Render(%s) value=%q error=%v", locale, value, err)
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
