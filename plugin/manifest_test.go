package springboot

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	sdk "github.com/lemon-paperplane/lemon-paperplane-main/lemon-paperplane-main-plugin-sdk"
)

// 校验 plugin.json：规则正则可编译、文案 key 在两份 l10n 里都有、图标只用允许的 SVG 子集。
func TestManifest(t *testing.T) {
	m, err := sdk.LoadManifest("..")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile("../plugin.json")
	keys := regexp.MustCompile(`"%([\w.-]+)%"`).FindAllStringSubmatch(string(raw), -1)
	for _, loc := range []string{"en", "zh-Hans"} {
		var l map[string]string
		b, err := os.ReadFile(filepath.Join("..", "l10n", loc+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &l); err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		for _, k := range keys {
			if _, ok := l[k[1]]; !ok {
				t.Errorf("%s 缺少 %s", loc, k[1])
			}
		}
	}
	for _, r := range m.Contributes.Diagnostics {
		if _, err := regexp.Compile(r.Pattern); err != nil {
			t.Errorf("%s: %v", r.ID, err)
		}
		if r.Hint == "" {
			t.Errorf("%s 没有 hint", r.ID)
		}
	}
	for _, u := range m.Contributes.URLPatterns {
		if _, err := regexp.Compile(u.Pattern); err != nil {
			t.Errorf("url %s: %v", u.Pattern, err)
		}
	}
	allowed := map[string]bool{"svg": true, "g": true, "path": true, "circle": true, "ellipse": true, "rect": true, "line": true, "polyline": true, "polygon": true}
	attrs := map[string]bool{"viewBox": true, "fill": true, "stroke": true, "stroke-width": true, "stroke-linecap": true, "stroke-linejoin": true, "fill-rule": true, "opacity": true, "transform": true, "d": true, "xmlns": true, "cx": true, "cy": true, "r": true, "rx": true, "ry": true, "x": true, "y": true, "width": true, "height": true, "x1": true, "y1": true, "x2": true, "y2": true, "points": true}
	icons, _ := filepath.Glob("../icons/*.svg")
	for _, f := range icons {
		b, _ := os.ReadFile(f)
		dec := xml.NewDecoder(strings.NewReader(string(b)))
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			if se, ok := tok.(xml.StartElement); ok {
				if !allowed[se.Name.Local] {
					t.Errorf("%s: 不允许的元素 %s", f, se.Name.Local)
				}
				for _, a := range se.Attr {
					if !attrs[a.Name.Local] {
						t.Errorf("%s: 不允许的属性 %s", f, a.Name.Local)
					}
				}
			}
		}
	}
}

// 校验代码里用到的 ctx.T key 在两份 l10n 里都有。
func TestCodeL10nKeys(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	re := regexp.MustCompile(`\.T\("([\w.-]+)"`)
	var used []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			used = append(used, m[1])
		}
	}
	for _, loc := range []string{"en", "zh-Hans"} {
		var l map[string]string
		b, _ := os.ReadFile(filepath.Join("..", "l10n", loc+".json"))
		_ = json.Unmarshal(b, &l)
		for _, k := range used {
			if _, ok := l[k]; !ok {
				t.Errorf("%s 缺少 %s", loc, k)
			}
		}
	}
}
