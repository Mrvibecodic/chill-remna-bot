package assets

import (
	"io/fs"
	"strings"
	"testing"
)

var allSectionKeys = []string{
	SectionBuySubscription, SectionMySubscription, SectionReferral, SectionPromoCode,
	SectionMainMenu, SectionAdminStats,
}

func TestEverySectionHasImage(t *testing.T) {
	for _, key := range allSectionKeys {
		if !Has(key) {
			t.Errorf("у раздела %q нет картинки", key)
		}
	}
}

// Картинка в сборке, которую не показывает ни один раздел, — мёртвый груз.
func TestNoUnusedImages(t *testing.T) {
	used := map[string]bool{}
	for _, key := range allSectionKeys {
		used[key+".jpg"] = true
	}
	entries, err := fs.ReadDir(sectionsFS, "sections")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !used[e.Name()] {
			t.Errorf("картинка %s не используется ни одним разделом", e.Name())
		}
	}
}

func TestUserSectionsHaveLabels(t *testing.T) {
	list := UserSections()
	if len(list) != 4 {
		t.Fatalf("клиентских разделов %d, ожидалось 4", len(list))
	}
	for _, sec := range list {
		for _, lang := range []string{"ru", "en"} {
			if label := LabelByKey(sec.Key, lang); label == sec.Key || strings.TrimSpace(label) == "" {
				t.Errorf("у раздела %q нет подписи (%s)", sec.Key, lang)
			}
		}
	}
}
