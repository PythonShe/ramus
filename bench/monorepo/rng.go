package main

import (
	"fmt"
	"math"
	"strings"
)

// What strings a file draws on: plain English words, any script at random, or
// one script from the scripts table (its index).
const (
	scEnglish = -2
	scMixed   = -1
)

// rng is splitmix64. It is written out here so the stream never changes with
// the Go version.
type rng struct {
	s  uint64
	sc int
}

func (r *rng) u64() uint64 {
	r.s += 0x9e3779b97f4a7c15
	z := r.s
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (r *rng) n(n int) int            { return int(r.u64() % uint64(n)) }
func (r *rng) f() float64             { return float64(r.u64()>>11) / (1 << 53) }
func (r *rng) pick(s []string) string { return s[r.n(len(s))] }
func (r *rng) norm() float64 {
	return math.Sqrt(-2*math.Log(1-r.f())) * math.Cos(2*math.Pi*r.f())
}

func (r *rng) snake() string { return r.pick(words) + "_" + r.pick(words) }
func (r *rng) camel() string {
	b := r.pick(words)
	return r.pick(words) + strings.ToUpper(b[:1]) + b[1:]
}
func (r *rng) pascal() string {
	c := r.camel()
	return strings.ToUpper(c[:1]) + c[1:]
}

// phrase is a sentence in the file's script, in any script for mixed files,
// or plain words for English-only files.
func (r *rng) phrase() string {
	sc := r.sc
	if sc == scEnglish {
		return r.pick(words) + " " + r.pick(words) + " " + r.pick(words)
	}
	if sc < 0 {
		sc = r.n(len(scripts))
	}
	return r.pick(scripts[sc].phrases)
}

// str is what goes into string literals and comments.
func (r *rng) str() string {
	switch {
	case r.sc == scMixed && r.n(4) == 0, r.sc >= 0 && r.n(5) < 3:
		return r.phrase()
	}
	return r.pick(words) + " " + r.pick(words)
}

// expand fills a template: $a camelCase, $u snake_case, $N PascalCase,
// $n number, $w word, $s string, $p phrase, $$ a dollar sign.
func (r *rng) expand(t string) string {
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		if t[i] != '$' || i+1 == len(t) {
			b.WriteByte(t[i])
			continue
		}
		i++
		switch t[i] {
		case 'a':
			b.WriteString(r.camel())
		case 'u':
			b.WriteString(r.snake())
		case 'N':
			b.WriteString(r.pascal())
		case 'n':
			fmt.Fprintf(&b, "%d", r.n(4096))
		case 'w':
			b.WriteString(r.pick(words))
		case 's':
			b.WriteString(r.str())
		case 'p':
			b.WriteString(r.phrase())
		case '$':
			b.WriteByte('$')
		default:
			b.WriteByte('$')
			b.WriteByte(t[i])
		}
	}
	return b.String()
}

// fileName follows each language's naming habit. One file in fifty, and every
// file of a single-script folder (script set), is named in a non-Latin script.
func (r *rng) fileName(ext string, sc int, script bool) string {
	if script || r.n(50) == 0 {
		if sc < 0 {
			sc = r.n(len(scripts) - 1) // not the symbols row
		}
		if scripts[sc].tag != "symbols" {
			name := strings.NewReplacer(" ", "-", "/", "-", ":", "-").Replace(r.pick(scripts[sc].phrases))
			return fmt.Sprintf("%s-%d.%s", name, r.n(1000), ext)
		}
	}
	if r.n(25) == 0 {
		if known, ok := wellKnown[ext]; ok {
			return known
		}
	}
	var name string
	switch ext {
	case "java", "kt", "swift", "cs", "tsx":
		name = r.pascal()
	case "js", "ts", "css", "html", "json", "md", "xml", "png", "cc", "php":
		name = r.pick(words) + "-" + r.pick(words)
	default:
		name = r.snake()
	}
	switch {
	case ext == "go" && r.n(6) == 0:
		name += "_test"
	case (ext == "ts" || ext == "js") && r.n(8) == 0:
		name += ".spec"
	case ext == "py" && r.n(8) == 0:
		name = "test_" + name
	case ext == "cc" && r.n(8) == 0:
		name += "_unittest"
	}
	return name + "." + ext
}

var wellKnown = map[string]string{
	"md": "README.md", "json": "package.json", "ts": "index.ts", "js": "index.js",
	"py": "__init__.py", "rs": "mod.rs", "txt": "CMakeLists.txt", "sh": "Makefile",
	"yaml": "docker-compose.yaml", "toml": "Cargo.toml", "go": "doc.go", "html": "index.html",
}

var areas = []struct {
	path, exts string
	share      int
}{
	{"apps/web/src", "ts tsx css json html png md", 9},
	{"apps/mobile-ios/Sources", "swift json png md", 4},
	{"apps/mobile-android/app/src/main", "kt java xml png", 5},
	{"apps/desktop", "cc h cs png", 5},
	{"services", "go proto yaml sql sh md", 14},
	{"libs/native", "c h cc rs", 12},
	{"libs/python", "py toml txt md", 8},
	{"libs/js", "js ts json md", 8},
	{"third_party", "c h cc js py rb php lua md txt", 18},
	{"tools", "sh py rb go lua", 4},
	{"docs", "md txt png", 5},
	{"config", "yaml toml json xml", 3},
	{"data", "csv json sql txt", 5},
}

var authors = []struct{ name, email, tz string }{
	{"Ada Fixture", "ada@example.invalid", "+0000"},
	{"山田 太郎", "taro@example.invalid", "+0900"},
	{"王小明", "xiaoming@example.invalid", "+0800"},
	{"김민준", "minjun@example.invalid", "+0900"},
	{"Zoë Müller-Lüdenscheidt", "zoe@example.invalid", "+0100"},
	{"محمد الفاسي", "mohammed@example.invalid", "+0300"},
	{"Иван Петров", "ivan@example.invalid", "+0300"},
	{"דנה כהן", "dana@example.invalid", "+0200"},
	{"Søren Ærøskøbing", "soren@example.invalid", "+0100"},
	{"Nguyễn Thị Hương", "huong@example.invalid", "+0700"},
	{"José Ñandú", "jose@example.invalid", "-0300"},
	{"प्रिया शर्मा", "priya@example.invalid", "+0530"},
	{"dependabot[bot]", "bot@example.invalid", "+0000"},
	{"\U0001f680 Release Bot", "release@example.invalid", "-0800"},
}

var words = strings.Fields(`account adapter agent alloc anchor api archive array assert async atlas audio auth backup batch beacon bench binary bitmap block bridge broker buffer build bundle cache canvas channel chart check chunk cipher client clock cluster codec color command commit compile config connect context control convert core counter crypto cursor daemon data decode delta deploy device diff digest dispatch document driver editor element encode engine entry error event exec export feature fetch field filter flag format frame gateway graph grid group handler hash header heap host image import index input item job kernel key layout ledger lexer link list loader lock log manifest map matrix media memory merge message meta metric model module monitor mutex network node notify object offset option output packet page parser patch path pipeline pixel plugin pointer policy pool port process profile proxy query queue range reader record region registry render report request resolve resource result route rule runtime sample scanner schema scope script search segment select server service session shader signal slice socket source stack state status storage stream string struct style symbol sync table target task template thread timer token trace tree type update upload user util value vector version view volume watch widget window worker writer zone`)

// scripts is the text the fixture draws on for names, comments, strings and
// commit messages. The last row is symbols and is never used for file names.
var scripts = []struct {
	tag     string
	phrases []string
}{
	{"zh-Hans", []string{"修复登录页面的崩溃问题", "更新依赖版本", "添加单元测试", "优化渲染性能", "重构网络模块", "中文文件名测试", "数据库迁移脚本", "用户界面调整"}},
	{"zh-Hant", []string{"修復記憶體洩漏", "更新說明文件", "新增設定選項", "繁體中文測試", "調整使用者介面"}},
	{"ja", []string{"ログイン画面のクラッシュを修正", "依存関係を更新", "テストを追加", "日本語のファイル名", "パフォーマンスを改善する", "ひらがなとカタカナとｶﾀｶﾅ"}},
	{"ko", []string{"로그인 화면 충돌 수정", "의존성 업데이트", "테스트 추가", "한국어 파일 이름", "성능 개선"}},
	{"ar", []string{"إصلاح خطأ في صفحة تسجيل الدخول", "تحديث المكتبات", "إضافة اختبارات", "ملف باللغة العربية"}},
	{"he", []string{"תיקון קריסה במסך הכניסה", "עדכון תלויות", "הוספת בדיקות", "קובץ בעברית"}},
	{"hi", []string{"लॉगिन पृष्ठ की त्रुटि ठीक की", "निर्भरता अद्यतन", "परीक्षण जोड़े", "हिंदी फ़ाइल नाम"}},
	{"bn", []string{"লগইন পৃষ্ঠার ত্রুটি সংশোধন", "পরীক্ষা যোগ করা হয়েছে"}},
	{"ta", []string{"உள்நுழைவு பிழை சரிசெய்யப்பட்டது", "சோதனைகள் சேர்க்கப்பட்டன"}},
	{"th", []string{"แก้ไขข้อผิดพลาดหน้าเข้าสู่ระบบ", "อัปเดตไลบรารี", "เพิ่มการทดสอบ", "ชื่อไฟล์ภาษาไทย"}},
	{"ru", []string{"Исправлена ошибка на странице входа", "Обновление зависимостей", "Добавлены тесты", "Файл на русском языке"}},
	{"el", []string{"Διόρθωση σφάλματος στη σελίδα σύνδεσης", "Ενημέρωση εξαρτήσεων", "Προσθήκη δοκιμών"}},
	{"vi", []string{"Sửa lỗi trang đăng nhập", "Cập nhật thư viện", "Thêm kiểm thử", "Tên tệp tiếng Việt"}},
	{"ka", []string{"შესვლის გვერდის შეცდომის გასწორება", "ტესტების დამატება"}},
	{"hy", []string{"Մուտքի էջի սխալի ուղղում", "Թեստերի ավելացում"}},
	{"am", []string{"የመግቢያ ገጽ ስህተት ተስተካክሏል", "ሙከራዎች ታክለዋል"}},
	{"emoji", []string{"🚀 ship it ✨", "🐛 fix 🔥 hot path", "👨‍👩‍👧‍👦 family 🏳️‍🌈 flag 👍🏽 tone", "🇯🇵🇫🇷🇧🇷 regional", "✅ ❌ ⚠️ ℹ️ ♻️"}},
	{"symbols", []string{
		"∀x∈ℝ: x² ≥ 0 ∧ √x ≤ ∞", "→ ← ↑ ↓ ⇒ ⇔ ≠ ≈ ≤ ≥", "┌─┬─┐ │ │ │ └─┴─┘ ▓▒░", "½ ⅓ ¼ ‰ © ® ™ § ¶ † ‡",
		"ﬁ ﬂ ß ẞ İ ı ǅ", "𠮷野家 𝒜𝓑𝒞 𝔘𝔫𝔦 🄰🄱 𓀀𓀁", "ｆｕｌｌｗｉｄｔｈ　ＴＥＸＴ",
		"Z̷̛a͜l̵g̸o̶ é å ñ combining",
		"‏RLM‎ LRM ‮RLO‬ ⁦LRI⁩ zero​width",
	}},
}
