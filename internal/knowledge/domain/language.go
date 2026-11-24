package domain

import (
	"strings"
	"unicode/utf8"

	"github.com/abadojack/whatlanggo"
)

const minLangDetectRunes = 50

// pgCatalog maps the ISO 639-3 codes whatlanggo emits (verified against
// its v1.0.1 lang.go: Arabic is arb, Norwegian is nob, Yiddish is ydd) to
// built-in PostgreSQL text search configurations. Languages the detector
// never reports are absent; unsupported detections use simple.
var pgCatalog = map[string]string{
	"arb": "pg_catalog.arabic",
	"dan": "pg_catalog.danish",
	"nld": "pg_catalog.dutch",
	"eng": "pg_catalog.english",
	"fin": "pg_catalog.finnish",
	"fra": "pg_catalog.french",
	"deu": "pg_catalog.german",
	"ell": "pg_catalog.greek",
	"hin": "pg_catalog.hindi",
	"hun": "pg_catalog.hungarian",
	"ind": "pg_catalog.indonesian",
	"ita": "pg_catalog.italian",
	"lit": "pg_catalog.lithuanian",
	"nep": "pg_catalog.nepali",
	"nno": "pg_catalog.norwegian",
	"nob": "pg_catalog.norwegian",
	"por": "pg_catalog.portuguese",
	"ron": "pg_catalog.romanian",
	"rus": "pg_catalog.russian",
	"srp": "pg_catalog.serbian",
	"spa": "pg_catalog.spanish",
	"swe": "pg_catalog.swedish",
	"tam": "pg_catalog.tamil",
	"tur": "pg_catalog.turkish",
	"ydd": "pg_catalog.yiddish",
}

// DetectLanguage returns a PostgreSQL regconfig name for the source body.
func DetectLanguage(source string) string {
	if utf8.RuneCountInString(strings.TrimSpace(source)) < minLangDetectRunes {
		return "pg_catalog.simple"
	}
	info := whatlanggo.Detect(source)
	if !info.IsReliable() {
		return "pg_catalog.simple"
	}
	iso := info.Lang.Iso6393()
	if cfg, ok := pgCatalog[iso]; ok {
		return cfg
	}
	return "pg_catalog.simple"
}
