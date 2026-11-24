package domain

import "testing"

func TestDetectLanguageMapsToBuiltInConfigurations(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "nynorsk maps to the norwegian configuration",
			text: "Eg veit ikkje kva dei vil ha, men dei sa det heilt klart i går kveld. " +
				"Mange av ungane i bygda gjekk heim tidleg fordi det hadde vorte mørkt ute.",
			want: "pg_catalog.norwegian",
		},
		{
			name: "dutch",
			text: "De werkgroep presentatie van de ontwikkelaars bevat een handleiding " +
				"voor het installeren van de software op verschillende systemen. " +
				"De meeste gebruikers lezen de handleiding en volgen de stappen " +
				"zonder problemen, maar sommige onderdelen vragen extra aandacht.",
			want: "pg_catalog.dutch",
		},
		{
			name: "arabic",
			text: "هذا نص تجريبي طويل يصف كيفية تثبيت البرنامج على الخادم. " +
				"يجب تحميل الحزمة ثم تشغيل الأمر من سطر الأوامر. " +
				"بعد الانتهاء يعاد تشغيل النظام مرة واحدة.",
			want: "pg_catalog.arabic",
		},
		{
			name: "english",
			text: "The quick brown fox jumps over the lazy dog while the kettle " +
				"holds tea for the team. Most users read the guide and follow " +
				"the steps without problems, but some sections need attention " +
				"and a careful review of every installation step.",
			want: "pg_catalog.english",
		},
		{
			name: "long but unreliable text falls back",
			text: "the das de la el und por и sur mit met für je tu vous wir " +
				"los das het de el и на с με naar van met von с",
			want: "pg_catalog.simple",
		},
		{
			name: "short but reliable text falls back",
			text: "This is a short English sentence about tea.",
			want: "pg_catalog.simple",
		},
		{
			name: "unsupported language falls back",
			text: "こんにちは世界。これはテストです。日本語のテキスト検索設定はPostgreSQLに" +
				"組み込まれていません。そのためこの文章はシンプルな設定を使います。" +
				"さらに文章を足して信頼できる検出を確実にします。",
			want: "pg_catalog.simple",
		},
		{
			name: "short text falls back",
			text: "Te kort voor een betrouwbare meting.",
			want: "pg_catalog.simple",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectLanguage(tc.text); got != tc.want {
				t.Fatalf("DetectLanguage = %q, want %q", got, tc.want)
			}
		})
	}
}
