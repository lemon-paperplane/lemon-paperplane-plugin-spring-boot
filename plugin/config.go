package springboot

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// 极简 YAML / properties 读取：只取 Spring 配置需要的「点分键 → 字符串」

// configDoc 是配置文件里的一个文档（YAML 用 --- 分隔多个文档）。
type configDoc struct {
	File     string
	Profiles []string          // 只在这些 profile 生效（application-dev.yml 或 on-profile 文档）；空表示基础配置
	Values   map[string]string // 规范化键（小写、去掉 - 和 _）→ 值
	Raw      map[string]string // 原始键 → 值（用于展示）
}

// normKey 实现 Spring 宽松绑定的键比较：小写并去掉 - 与 _。
func normKey(k string) string {
	k = strings.ToLower(k)
	return strings.NewReplacer("-", "", "_", "").Replace(k)
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
		return v[1 : len(v)-1]
	}
	return v
}

// stripInlineComment 去掉引号外的 " #" 注释。
func stripInlineComment(v string) string {
	inS, inD := false, false
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '\'':
			if !inD {
				inS = !inS
			}
		case '"':
			if !inS {
				inD = !inD
			}
		case '#':
			if !inS && !inD && (i == 0 || v[i-1] == ' ' || v[i-1] == '\t') {
				return strings.TrimSpace(v[:i])
			}
		}
	}
	return strings.TrimSpace(v)
}

// parseYAML 把 YAML 拍平成点分键。支持嵌套映射、标量列表（合并为逗号分隔）、多文档、块标量。
func parseYAML(text string) []map[string]string {
	var docs []map[string]string
	cur := map[string]string{}
	type frame struct {
		indent int
		key    string
	}
	var stack []frame
	listIdx := map[string]int{}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	path := func(key string) string {
		parts := make([]string, 0, len(stack)+1)
		for _, f := range stack {
			parts = append(parts, f.key)
		}
		if key != "" {
			parts = append(parts, key)
		}
		return strings.Join(parts, ".")
	}
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		trim := strings.TrimSpace(raw)
		if trim == "---" || strings.HasPrefix(trim, "--- ") {
			if len(cur) > 0 {
				docs = append(docs, cur)
			}
			cur = map[string]string{}
			stack = nil
			continue
		}
		if trim == "" || strings.HasPrefix(trim, "#") || trim == "..." {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		if strings.HasPrefix(trim, "- ") || trim == "-" {
			item := stripInlineComment(strings.TrimSpace(strings.TrimPrefix(trim, "-")))
			parent := path("")
			if k, v, ok := cutKey(item); ok {
				// 列表里的映射：parent[i].k
				idx := listIdx[parent]
				listIdx[parent] = idx + 1
				full := parent + "[" + strconv.Itoa(idx) + "]"
				if v != "" {
					cur[full+"."+k] = unquote(v)
				}
				continue
			}
			if item != "" {
				v := unquote(item)
				if old, ok := cur[parent]; ok && old != "" {
					cur[parent] = old + "," + v
				} else {
					cur[parent] = v
				}
			}
			continue
		}
		k, v, ok := cutKey(trim)
		if !ok {
			continue
		}
		v = stripInlineComment(v)
		switch {
		case v == "":
			stack = append(stack, frame{indent, k})
		case v == "|" || v == ">" || v == "|-" || v == ">-" || v == "|+" || v == ">+":
			var block []string
			for i+1 < len(lines) {
				next := lines[i+1]
				nIndent := len(next) - len(strings.TrimLeft(next, " \t"))
				if strings.TrimSpace(next) != "" && nIndent <= indent {
					break
				}
				block = append(block, strings.TrimSpace(next))
				i++
			}
			cur[path(k)] = strings.TrimSpace(strings.Join(block, "\n"))
		case strings.HasPrefix(v, "["):
			items := strings.Split(strings.Trim(v, "[]"), ",")
			for j := range items {
				items[j] = unquote(items[j])
			}
			cur[path(k)] = strings.Join(items, ",")
		default:
			cur[path(k)] = unquote(v)
		}
	}
	if len(cur) > 0 {
		docs = append(docs, cur)
	}
	return docs
}

// cutKey 把 "key: value" 切开；键可以带引号。
func cutKey(s string) (string, string, bool) {
	if s == "" {
		return "", "", false
	}
	if s[0] == '"' || s[0] == '\'' {
		end := strings.IndexByte(s[1:], s[0])
		if end < 0 {
			return "", "", false
		}
		key := s[1 : end+1]
		rest := strings.TrimSpace(s[end+2:])
		if !strings.HasPrefix(rest, ":") {
			return "", "", false
		}
		return key, strings.TrimSpace(rest[1:]), true
	}
	i := strings.Index(s, ":")
	for i >= 0 && i+1 < len(s) && s[i+1] != ' ' && s[i+1] != '\t' {
		// "http://x" 之类值里的冒号不是键分隔符
		j := strings.Index(s[i+1:], ":")
		if j < 0 {
			i = -1
			break
		}
		i += j + 1
	}
	if i <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(s[:i])
	if strings.ContainsAny(key, " {}[]") {
		return "", "", false
	}
	return key, strings.TrimSpace(s[i+1:]), true
}

// parseProperties 解析 .properties（含多文档分隔 #--- 与续行）。
func parseProperties(text string) []map[string]string {
	var docs []map[string]string
	cur := map[string]string{}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "#---" || line == "!---" {
			docs = append(docs, cur)
			cur = map[string]string{}
			continue
		}
		for strings.HasSuffix(line, `\`) && i+1 < len(lines) {
			i++
			line = strings.TrimSuffix(line, `\`) + strings.TrimSpace(lines[i])
		}
		if line == "" || line[0] == '#' || line[0] == '!' {
			continue
		}
		idx := strings.IndexAny(line, "=:")
		if idx < 0 {
			continue
		}
		cur[strings.TrimSpace(line[:idx])] = strings.TrimSpace(line[idx+1:])
	}
	docs = append(docs, cur)
	return docs
}

var (
	profileFileRe = regexp.MustCompile(`^(application|bootstrap)(?:-([\w.\-]+))?\.(ya?ml|properties)$`)
	placeholderRe = regexp.MustCompile(`\$\{([^{}:]+)(?::([^{}]*))?\}`)
)

// loadConfigs 读取资源目录里的 application*/bootstrap* 配置文件。
func loadConfigs(dirs []string) []configDoc {
	var out []configDoc
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var names []string
		for _, e := range entries {
			if !e.IsDir() && profileFileRe.MatchString(e.Name()) {
				names = append(names, e.Name())
			}
		}
		// bootstrap 先于 application，基础文件先于 profile 文件
		sort.SliceStable(names, func(i, j int) bool { return configOrder(names[i]) < configOrder(names[j]) })
		for _, n := range names {
			m := profileFileRe.FindStringSubmatch(n)
			data, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil || len(data) > 1<<20 {
				continue
			}
			var docs []map[string]string
			if m[3] == "properties" {
				docs = parseProperties(string(data))
			} else {
				docs = parseYAML(string(data))
			}
			for _, d := range docs {
				doc := configDoc{File: filepath.Join(dir, n), Values: map[string]string{}, Raw: d}
				for k, v := range d {
					doc.Values[normKey(k)] = v
				}
				if m[2] != "" {
					doc.Profiles = []string{m[2]}
				}
				// 文档级 profile 条件：spring.config.activate.on-profile（2.4+）或 spring.profiles（老写法）
				if on := firstNonEmpty(doc.Values[normKey("spring.config.activate.on-profile")], doc.Values[normKey("spring.profiles")]); on != "" && m[2] == "" {
					for _, p := range strings.Split(on, ",") {
						p = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p), "!"))
						if p != "" && !strings.ContainsAny(p, "&|()") {
							doc.Profiles = append(doc.Profiles, p)
						}
					}
				}
				out = append(out, doc)
			}
		}
	}
	return out
}

func configOrder(name string) int {
	n := 0
	if strings.HasPrefix(name, "application") {
		n += 10
	}
	if strings.Contains(name, "-") {
		n += 5
	}
	return n
}

// profilesOf 返回配置里出现的所有 profile（文件名与 on-profile 文档），排序去重。
func profilesOf(docs []configDoc) []string {
	set := map[string]bool{}
	for _, d := range docs {
		for _, p := range d.Profiles {
			set[p] = true
		}
		if g := d.Raw; g != nil {
			for k := range g {
				// spring.profiles.group.<name>
				if strings.HasPrefix(normKey(k), normKey("spring.profiles.group.")) {
					parts := strings.Split(k, ".")
					set[parts[len(parts)-1]] = true
				}
			}
		}
	}
	var out []string
	for p := range set {
		if p != "default" && !strings.Contains(p, "@") {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// effective 按生效 profile 合并配置：基础文档在前，profile 文档按 profile 顺序覆盖。
type effective struct {
	values map[string]string
	source map[string]string // 规范化键 → 来源文件名
}

func merge(docs []configDoc, profiles []string) *effective {
	e := &effective{values: map[string]string{}, source: map[string]string{}}
	apply := func(d configDoc) {
		for k, v := range d.Values {
			e.values[k] = v
			e.source[k] = filepath.Base(d.File)
		}
	}
	for _, d := range docs {
		if len(d.Profiles) == 0 {
			apply(d)
		}
	}
	for _, p := range profiles {
		for _, d := range docs {
			for _, dp := range d.Profiles {
				if dp == p {
					apply(d)
				}
			}
		}
	}
	return e
}

// get 取值并展开 ${key:default} 占位符（环境变量不可知，取默认值）。
func (e *effective) get(key string) string {
	return e.resolve(e.values[normKey(key)], 0)
}

func (e *effective) has(key string) bool {
	_, ok := e.values[normKey(key)]
	return ok
}

func (e *effective) src(key string) string { return e.source[normKey(key)] }

func (e *effective) resolve(v string, depth int) string {
	if depth > 5 || !strings.Contains(v, "${") {
		return v
	}
	// 先处理最内层
	for i := 0; i < 10 && strings.Contains(v, "${"); i++ {
		next := placeholderRe.ReplaceAllStringFunc(v, func(m string) string {
			sm := placeholderRe.FindStringSubmatch(m)
			if x, ok := e.values[normKey(sm[1])]; ok {
				return e.resolve(x, depth+1)
			}
			if strings.Contains(m, ":") {
				return sm[2]
			}
			return m
		})
		if next == v {
			break
		}
		v = next
	}
	return v
}

// defaultProfiles 读基础配置里的 spring.profiles.active（Maven 资源过滤占位符 @x@ 忽略）。
func defaultProfiles(docs []configDoc) []string {
	e := merge(docs, nil)
	v := e.get("spring.profiles.active")
	if v == "" || strings.Contains(v, "@") || strings.Contains(v, "${") {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// serverInfo 返回端口与上下文路径（默认 8080 与空路径）。
func serverInfo(e *effective) (int, string) {
	port := 8080
	if v := e.get("server.port"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			port = n
		}
	}
	ctx := firstNonEmpty(e.get("server.servlet.context-path"), e.get("server.context-path"), e.get("spring.webflux.base-path"))
	ctx = strings.TrimSpace(ctx)
	if ctx == "/" {
		ctx = ""
	}
	if ctx != "" && !strings.HasPrefix(ctx, "/") {
		ctx = "/" + ctx
	}
	return port, strings.TrimRight(ctx, "/")
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
